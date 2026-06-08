// Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package remediation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/nvidia/nvsentinel/commons/pkg/managed"
	"github.com/nvidia/nvsentinel/commons/pkg/tracing"
	"github.com/nvidia/nvsentinel/data-models/pkg/model"
	"github.com/nvidia/nvsentinel/data-models/pkg/protos"
	"github.com/nvidia/nvsentinel/fault-remediation/pkg/annotation"
	"github.com/nvidia/nvsentinel/fault-remediation/pkg/common"
	"github.com/nvidia/nvsentinel/fault-remediation/pkg/config"
	"github.com/nvidia/nvsentinel/fault-remediation/pkg/crstatus"
	"github.com/nvidia/nvsentinel/fault-remediation/pkg/events"
	"github.com/nvidia/nvsentinel/fault-remediation/pkg/metrics"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	logCollectorNodeLabel              = "dgxc.nvidia.com/node-name"
	logCollectorEventLabel             = "dgxc.nvidia.com/event-id"
	jobMetricsAlreadyCountedAnnotation = "dgxc.nvidia.com/logcollector-metrics-recorded"
	trueStringVal                      = "true"
	controllerRequeueDuration          = 10 * time.Second
	labelValueCharactersLimit          = 63

	// Environment variable names
	LogCollectorManifestPathEnv = "LOG_COLLECTOR_MANIFEST_PATH"
)

type FaultRemediationClient struct {
	client     client.Client
	dryRunMode []string

	// Multi-template support
	remediationConfig config.TomlConfig
	templates         map[string]*template.Template // map from template file name to parsed template
	templateMountPath string

	annotationManager annotation.NodeAnnotationManagerInterface
	statusChecker     *crstatus.CRStatusChecker
}

func NewRemediationClient(
	client client.Client,
	dryRun bool,
	remediationConfig config.TomlConfig,
) (*FaultRemediationClient, error) {
	// Determine template mount path
	templateMountPath := remediationConfig.Template.MountPath
	if templateMountPath == "" {
		return nil, fmt.Errorf("template mount path is not configured")
	}

	// Pre-load and parse all templates
	templates := make(map[string]*template.Template)

	// Load templates for multi-template actions
	for actionName, maintenanceResource := range remediationConfig.RemediationActions {
		if maintenanceResource.TemplateFileName == "" {
			return nil, fmt.Errorf("remediation action %s is missing template file configuration", actionName)
		}

		tmpl, err := loadAndParseTemplate(templateMountPath, maintenanceResource.TemplateFileName, actionName)
		if err != nil {
			return nil, fmt.Errorf("failed to load template for action %s: %w", actionName, err)
		}

		templates[actionName] = tmpl
	}

	// Validate namespace configuration for namespaced resources
	for actionName, maintenanceResource := range remediationConfig.RemediationActions {
		if maintenanceResource.Scope == "Namespaced" && maintenanceResource.Namespace == "" {
			return nil, fmt.Errorf("remediation action %s is namespaced but missing namespace configuration", actionName)
		}
	}

	ctrlRuntimeRemediationClient := &FaultRemediationClient{
		client:            client,
		templates:         templates,
		templateMountPath: templateMountPath,
		remediationConfig: remediationConfig,
	}

	if dryRun {
		ctrlRuntimeRemediationClient.dryRunMode = []string{metav1.DryRunAll}
	} else {
		ctrlRuntimeRemediationClient.dryRunMode = []string{}
	}

	// Initialize annotation manager
	ctrlRuntimeRemediationClient.annotationManager = annotation.NewNodeAnnotationManager(client)

	ctrlRuntimeRemediationClient.statusChecker = crstatus.NewCRStatusChecker(
		client,
		remediationConfig.RemediationActions,
		dryRun,
	)

	return ctrlRuntimeRemediationClient, nil
}

// loadAndParseTemplate loads and parses a template file
func loadAndParseTemplate(mountPath, fileName, templateName string) (*template.Template, error) {
	templatePath := filepath.Join(mountPath, fileName)

	// Check if the template file exists
	if _, err := os.Stat(templatePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("template file does not exist: %s", templatePath)
	}

	// Read and parse the template
	templateContent, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("error reading template file: %w", err)
	}

	tmpl := template.New(templateName)

	tmpl, err = tmpl.Parse(string(templateContent))
	if err != nil {
		return nil, fmt.Errorf("error parsing template: %w", err)
	}

	return tmpl, nil
}

func (c *FaultRemediationClient) GetAnnotationManager() annotation.NodeAnnotationManagerInterface {
	return c.annotationManager
}

func (c *FaultRemediationClient) GetStatusChecker() crstatus.CRStatusCheckerInterface {
	return c.statusChecker
}

func (c *FaultRemediationClient) CreateMaintenanceResource(ctx context.Context, healthEventData *events.HealthEventData,
	groupConfig *common.EquivalenceGroupConfig) (string, error) {
	healthEvent := healthEventData.HealthEvent
	healthEventID := healthEventData.ID

	ctx, span := tracing.StartSpan(ctx, "fault_remediation.create_maintenance_resource")
	defer span.End()

	// Generate CR name. For most CRD kinds this is informational (the actual
	// name comes from the rendered template); for ERR the helper produces a
	// taint-value-safe name that the ERR template later reads from
	// TemplateData.CRName.
	recommendedActionForName := model.GetEffectiveActionName(healthEvent)

	resourceForName, ok := c.remediationConfig.RemediationActions[recommendedActionForName]
	if !ok {
		// Fall back to the legacy pattern; the downstream
		// selectRemediationActionAndTemplate will surface the lookup error
		// with full context.
		resourceForName = config.MaintenanceResource{}
	}

	crName := computeCRName(resourceForName, healthEvent.NodeName, healthEventID)

	// Skip custom resource creation if dry-run is enabled
	if len(c.dryRunMode) > 0 {
		slog.InfoContext(ctx, "DRY-RUN: Skipping custom resource creation", "node", healthEvent.NodeName)
		span.SetAttributes(
			attribute.String("fault_remediation.cr_creation_skipped_reason", "dry run is enabled"),
		)

		return crName, nil
	}

	recommendedActionName := model.GetEffectiveActionName(healthEvent)

	maintenanceResource, selectedTemplate, actionKey, err :=
		c.selectRemediationActionAndTemplate(ctx, recommendedActionName, healthEvent.NodeName)
	if err != nil {
		tracing.RecordError(span, err)
		span.SetAttributes(
			attribute.String("fault_remediation.error.type", "remediation_action_and_template_selection_error"),
			attribute.String("fault_remediation.error.message", err.Error()),
		)

		return "", fmt.Errorf("error selecting remediation action and template: %w", err)
	}

	node, err := c.getNodeForOwnerReference(ctx, healthEvent.NodeName)
	if err != nil {
		slog.WarnContext(ctx, "Failed to get node for owner reference, skipping CR creation",
			"node", healthEvent.NodeName,
			"error", err)

		span.AddEvent("Failed to get node for owner reference, skipping CR creation", trace.WithAttributes(
			attribute.String("fault_remediation.error.type", "node_owner_reference_error"),
			attribute.String("fault_remediation.error.message", err.Error()),
		))

		return "", fmt.Errorf("failed to get node for owner reference: %w", err)
	}

	traceID := tracing.TraceIDFromMetadata(healthEvent.GetMetadata())
	templateData := templateDataFromEvent(healthEvent, healthEventID, traceID, tracing.SpanIDFromSpan(span),
		recommendedActionName, groupConfig.ImpactedEntityScopeValue, maintenanceResource)

	actualCRName, err := c.createMaintenanceCR(ctx, selectedTemplate, templateData, actionKey, node, healthEventData)
	if err != nil {
		tracing.RecordError(span, err)
		span.SetAttributes(
			attribute.String("fault_remediation.error.type", "create_maintenance_cr_error"),
			attribute.String("fault_remediation.error.message", err.Error()),
		)

		return "", err
	}

	if err := c.updateRemediationAnnotationIfNeeded(ctx, healthEvent.NodeName, groupConfig.EffectiveEquivalenceGroup,
		actualCRName, recommendedActionName); err != nil {
		tracing.RecordError(span, err)
		span.SetAttributes(
			attribute.String("fault_remediation.error.type", "update_remediation_annotation_error"),
			attribute.String("fault_remediation.error.message", err.Error()),
		)

		return "", err
	}

	span.SetAttributes(
		attribute.String("fault_remediation.cr.name", actualCRName),
		attribute.String("fault_remediation.cr.template", actionKey),
	)

	return actualCRName, nil
}

// templateDataFromEvent builds TemplateData from health event and maintenance resource.
func templateDataFromEvent(healthEvent *protos.HealthEvent, healthEventID, traceID, spanID, recommendedActionName,
	impactedEntityScopeValue string, maintenanceResource config.MaintenanceResource,
) TemplateData {
	td := TemplateData{
		NodeName:                 healthEvent.NodeName,
		HealthEventID:            healthEventID,
		TraceID:                  traceID,
		SpanID:                   spanID,
		RecommendedAction:        healthEvent.RecommendedAction,
		RecommendedActionName:    recommendedActionName,
		ImpactedEntityScopeValue: impactedEntityScopeValue,
		HealthEvent:              healthEvent,
		ApiGroup:                 maintenanceResource.ApiGroup,
		Version:                  maintenanceResource.Version,
		Kind:                     maintenanceResource.Kind,
		Namespace:                maintenanceResource.Namespace,
		CRName:                   computeCRName(maintenanceResource, healthEvent.NodeName, healthEventID),
	}

	// The ERR template embeds the full HealthEvent inline; render it once via
	// protojson so the template doesn't need to know every proto field.
	if isExternalRemediationRequestResource(maintenanceResource) {
		if jsonBlob, err := protojson.Marshal(healthEvent); err == nil {
			td.HealthEventJSON = string(jsonBlob)
		} else {
			// Falling through with empty HealthEventJSON makes the template fail to
			// parse cleanly, which surfaces the bug instead of silently creating
			// an ERR with a missing spec. Log here so the error is observable.
			slog.Error("failed to marshal HealthEvent via protojson for ERR template",
				"healthEventID", healthEventID, "node", healthEvent.NodeName, "error", err)
		}
	}

	return td
}

const (
	// errCRNameMaxLen is the maximum length of an ERR's metadata.name,
	// bounded by Kubernetes' taint-value rules (label-value semantics:
	// ≤63 chars, [a-z0-9._-]). The ERR reconciler puts the name in the
	// release taint's value field, so anything longer would be rejected
	// by the apiserver at taint-application time.
	errCRNameMaxLen = 63

	// errCRNamePrefix keeps ERR CR names visually distinct from other
	// maintenance CRs in `kubectl get` output.
	errCRNamePrefix = "err-"

	// errCRNameHashLen is the hex-encoded length of the deterministic
	// suffix appended to keep the name globally unique per
	// (nodeName, healthEventID) regardless of how much the readable
	// nodename portion gets truncated.
	errCRNameHashLen = 8
)

// isExternalRemediationRequestResource is the dispatch predicate that selects
// ERR-specific code paths in this package.
func isExternalRemediationRequestResource(r config.MaintenanceResource) bool {
	return managed.IsERRResource(r.ApiGroup, r.Kind)
}

// computeCRName returns the CR's deterministic name. For ERR the name is
// constrained to the Kubernetes taint-value rules (≤63 chars, [a-z0-9._-])
// because the ERR reconciler uses it as the release taint's value. For all
// other CRD kinds we keep the established `maintenance-{nodeName}-{eventID}`
// pattern so existing templates that hardcode that string keep working.
func computeCRName(r config.MaintenanceResource, nodeName, healthEventID string) string {
	if !isExternalRemediationRequestResource(r) {
		return fmt.Sprintf("maintenance-%s-%s", nodeName, healthEventID)
	}

	return computeERRName(nodeName, healthEventID)
}

// computeERRName builds an ERR CR name as
// "err-<sanitised-truncated-nodename>-<8-hex-hash>" so the result is:
//
//   - deterministic given the same (nodeName, healthEventID) pair —
//     replays produce the same name and the create becomes an idempotent
//     no-op via apiserver AlreadyExists handling;
//   - ≤63 chars total, satisfying the taint-value length cap;
//   - composed only of taint-legal characters ([a-z0-9._-]);
//   - readable for operator debugging — the truncated nodename tells you
//     which node is held even when only the taint value is visible.
func computeERRName(nodeName, healthEventID string) string {
	hashBytes := sha256.Sum256([]byte(nodeName + "\x00" + healthEventID))
	hashHex := hex.EncodeToString(hashBytes[:])[:errCRNameHashLen]

	// reserved: prefix + "-" + hash
	reserved := len(errCRNamePrefix) + 1 + errCRNameHashLen
	maxNodePart := errCRNameMaxLen - reserved

	nodePart := sanitizeTaintValue(nodeName)
	if len(nodePart) > maxNodePart {
		nodePart = nodePart[:maxNodePart]
	}

	// Trim trailing separators so we don't leave the readable part ending in
	// "-" or "." (still legal but ugly).
	nodePart = strings.TrimRight(nodePart, "-._")
	if nodePart == "" {
		// Pathological case: a nodename that's entirely non-alphanumeric.
		// Falling back to just "err-<hash>" still respects the constraints.
		return errCRNamePrefix + hashHex
	}

	return errCRNamePrefix + nodePart + "-" + hashHex
}

// sanitizeTaintValue maps any character outside the taint-value allowlist
// to "-" so a node name containing e.g. ":" or "/" still produces a valid
// CR/taint-value name.
func sanitizeTaintValue(s string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	return b.String()
}

// createMaintenanceCR renders the template, sets owner ref, and creates the maintenance CR.
func (c *FaultRemediationClient) createMaintenanceCR(ctx context.Context, selectedTemplate *template.Template,
	templateData TemplateData, actionKey string, node *corev1.Node, healthEventData *events.HealthEventData,
) (string, error) {
	ctx, span := tracing.StartSpan(ctx, "fault_remediation.create_maintenance_cr")
	defer span.End()

	slog.InfoContext(ctx, "Creating maintenance CR",
		"node", healthEventData.HealthEvent.NodeName,
		"template", actionKey,
		"nodeUID", node.UID)

	maintenance, yamlStr, err := renderMaintenanceFromTemplate(selectedTemplate, templateData)
	if err != nil {
		tracing.RecordError(span, err)
		span.SetAttributes(
			attribute.String("fault_remediation.error.type", "render_maintenance_template_error"),
			attribute.String("fault_remediation.error.message", err.Error()),
		)
		slog.ErrorContext(ctx, "Failed to render maintenance template", "template", actionKey, "error", err)

		return "", fmt.Errorf("error rendering maintenance template: %w", err)
	}

	slog.DebugContext(ctx, "Generated YAML from template", "template", actionKey, "yaml", yamlStr)
	setNodeOwnerRef(ctx, maintenance, node)

	err = c.client.Create(ctx, maintenance)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			tracing.RecordError(span, err)
			span.SetAttributes(
				attribute.String("fault_remediation.error.type", "maintenance_cr_already_exists_error"),
				attribute.String("fault_remediation.error.message", err.Error()),
			)

			slog.InfoContext(ctx, "Maintenance CR already exists for node, treating as success",
				"CR", maintenance.GetName(), "node", healthEventData.HealthEvent.NodeName)

			return maintenance.GetName(), nil
		}

		tracing.RecordError(span, err)
		span.SetAttributes(
			attribute.String("fault_remediation.error.type", "create_maintenance_cr_error"),
			attribute.String("fault_remediation.error.message", err.Error()),
		)

		return "", fmt.Errorf("failed to create maintenance CR: %w", err)
	} else if healthEventData.HealthEventStatus != nil && healthEventData.HealthEventStatus.DrainFinishTimestamp != nil {
		duration := time.Since(healthEventData.HealthEventStatus.DrainFinishTimestamp.AsTime()).Seconds()
		if duration > 0 {
			slog.InfoContext(ctx, "Fault remediation CR generation duration",
				"duration", duration,
				"node", healthEventData.HealthEvent.NodeName)
			metrics.CRGenerationDuration.Observe(duration)
		}
	}

	slog.InfoContext(ctx, "Created Maintenance CR successfully",
		"crName", maintenance.GetName(), "node", healthEventData.HealthEvent.NodeName, "template", actionKey)

	return maintenance.GetName(), nil
}

// updateRemediationAnnotationIfNeeded updates node remediation state when equivalence group
// and annotation manager are set.
func (c *FaultRemediationClient) updateRemediationAnnotationIfNeeded(ctx context.Context, nodeName string,
	effectiveEquivalenceGroup string, actualCRName, recommendedActionName string,
) error {
	if effectiveEquivalenceGroup == "" || c.annotationManager == nil {
		return nil
	}

	ctx, span := tracing.StartSpan(ctx, "fault_remediation.update_remediation_annotation")
	defer span.End()

	span.SetAttributes(
		attribute.String("fault_remediation.node", nodeName),
		attribute.String("fault_remediation.equivalence_group", effectiveEquivalenceGroup),
		attribute.String("fault_remediation.cr.name", actualCRName),
	)

	err := c.annotationManager.UpdateRemediationState(ctx, nodeName, effectiveEquivalenceGroup,
		actualCRName, recommendedActionName)
	if err != nil {
		slog.WarnContext(ctx, "Failed to update node annotation", "node", nodeName, "error", err)
		tracing.RecordError(span, err)
		span.SetAttributes(attribute.String("fault_remediation.error.type", "update_node_annotation_error"))
		span.SetAttributes(attribute.String("fault_remediation.error.message", err.Error()))

		return fmt.Errorf("failed to update node annotation: %w", err)
	}

	return nil
}

func renderMaintenanceFromTemplate(
	tmpl *template.Template,
	data TemplateData,
) (*unstructured.Unstructured, string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, "", err
	}

	var obj map[string]any
	if err := yaml.Unmarshal(buf.Bytes(), &obj); err != nil {
		return nil, "", err
	}

	return &unstructured.Unstructured{Object: obj}, buf.String(), nil
}

func setNodeOwnerRef(ctx context.Context, maintenance *unstructured.Unstructured, node *corev1.Node) {
	ownerRef := metav1.OwnerReference{
		APIVersion:         "v1",
		Kind:               "Node",
		Name:               node.Name,
		UID:                node.UID,
		Controller:         ptr.To(false),
		BlockOwnerDeletion: ptr.To(false),
	}
	maintenance.SetOwnerReferences([]metav1.OwnerReference{ownerRef})

	slog.InfoContext(ctx, "Added owner reference to CR for automatic garbage collection",
		"node", node.Name,
		"nodeUID", node.UID,
		"crName", maintenance.GetName())
}

func (c *FaultRemediationClient) selectRemediationActionAndTemplate(
	ctx context.Context,
	recommendedActionName string,
	nodeName string,
) (config.MaintenanceResource, *template.Template, string, error) {
	resource, exists := c.remediationConfig.RemediationActions[recommendedActionName]
	if !exists {
		slog.ErrorContext(ctx, "No remediation configuration found for action",
			"action", recommendedActionName,
			"node", nodeName,
			"availableActions", func() []string {
				actions := make([]string, 0, len(c.remediationConfig.RemediationActions))
				for action := range c.remediationConfig.RemediationActions {
					actions = append(actions, action)
				}

				return actions
			}())

		return config.MaintenanceResource{}, nil, "", fmt.Errorf("no remediation config for action")
	}

	tmpl := c.templates[recommendedActionName]
	if tmpl == nil {
		slog.ErrorContext(ctx, "No template available for remediation action",
			"action", recommendedActionName,
			"node", nodeName)

		return config.MaintenanceResource{}, nil, "", fmt.Errorf("no template available for remediation action")
	}

	return resource, tmpl, recommendedActionName, nil
}

// getNodeForOwnerReference retrieves the node for setting owner reference on the CR.
func (c *FaultRemediationClient) getNodeForOwnerReference(
	ctx context.Context,
	nodeName string,
) (*corev1.Node, error) {
	ctx, span := tracing.StartSpan(ctx, "fault_remediation.get_node_for_owner_reference")
	defer span.End()

	node := &corev1.Node{}

	err := c.client.Get(ctx, types.NamespacedName{
		Name: nodeName,
	}, node)
	if err != nil {
		if apierrors.IsNotFound(err) {
			slog.DebugContext(ctx, "Node no longer exists, skipping CR creation", "node", nodeName)
			tracing.RecordError(span, err)
			span.SetAttributes(attribute.String("fault_remediation.error.type", "node_not_found"))
			span.SetAttributes(attribute.String("fault_remediation.error.message", err.Error()))

			return nil, fmt.Errorf("node not found: %w", err)
		}

		slog.ErrorContext(ctx, "Failed to get node", "node", nodeName, "error", err)
		tracing.RecordError(span, err)
		span.SetAttributes(attribute.String("fault_remediation.error.type", "get_node_error"))
		span.SetAttributes(attribute.String("fault_remediation.error.message", err.Error()))

		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	return node, nil
}

// RunLogCollectorJob creates a log collector Job and waits for completion.
func (c *FaultRemediationClient) RunLogCollectorJob(
	ctx context.Context,
	nodeName string,
	eventUID string,
) (ctrl.Result, error) {
	if len(c.dryRunMode) > 0 {
		slog.InfoContext(ctx, "DRY-RUN: Skipping log collector job for node", "node", nodeName)
		return ctrl.Result{}, nil
	}

	job, result, err := c.launchLogCollectorJob(ctx, nodeName, eventUID)
	if err != nil || !result.IsZero() {
		return result, err
	}

	return c.checkLogCollectorStatus(ctx, nodeName, job)
}

func (c *FaultRemediationClient) launchLogCollectorJob(
	ctx context.Context,
	nodeName string,
	eventUID string,
) (batchv1.Job, ctrl.Result, error) {
	ctx, span := tracing.StartSpan(ctx, "fault_remediation.launch_log_collector_job")
	defer span.End()

	// Read Job manifest
	manifestPath := os.Getenv(LogCollectorManifestPathEnv)
	if manifestPath == "" {
		manifestPath = filepath.Join(c.templateMountPath, "log-collector-job.yaml")
	}

	content, err := os.ReadFile(manifestPath)
	if err != nil {
		tracing.RecordError(span, err)
		span.SetAttributes(attribute.String("fault_remediation.error.type", "manifest_read_error"))
		span.SetAttributes(attribute.String("fault_remediation.error.message", err.Error()))
		metrics.LogCollectorErrors.WithLabelValues("manifest_read_error", nodeName).Inc()

		return batchv1.Job{}, ctrl.Result{}, fmt.Errorf("failed to read log collector manifest: %w", err)
	}

	// Create Job from manifest using strong types
	job := &batchv1.Job{}
	if err = yaml.Unmarshal(content, job); err != nil {
		tracing.RecordError(span, err)
		span.SetAttributes(attribute.String("fault_remediation.error.type", "manifest_unmarshal_error"))
		span.SetAttributes(attribute.String("fault_remediation.error.message", err.Error()))
		metrics.LogCollectorErrors.WithLabelValues("manifest_unmarshal_error", nodeName).Inc()

		return batchv1.Job{}, ctrl.Result{}, fmt.Errorf("failed to unmarshal Job manifest: %w", err)
	}

	// Set target node
	job.Spec.Template.Spec.NodeName = nodeName
	if job.Labels == nil {
		job.Labels = map[string]string{}
	}

	labels := map[string]string{
		logCollectorNodeLabel:  nodeName,
		logCollectorEventLabel: eventUID,
	}
	for k, v := range labels {
		job.Labels[k] = v
	}

	// Get job if exists otherwise create
	existingJobs := &batchv1.JobList{}

	err = c.client.List(
		ctx,
		existingJobs,
		client.MatchingLabels(labels),
		client.InNamespace(job.GetNamespace()),
	)
	if err != nil {
		tracing.RecordError(span, err)
		span.SetAttributes(attribute.String("fault_remediation.error.type", "list_log_collector_jobs_error"))
		span.SetAttributes(attribute.String("fault_remediation.error.message", err.Error()))

		return batchv1.Job{}, ctrl.Result{}, err
	}

	// There should not be multiple jobs for same event, in this case return error
	// this will then requeue and wait until the jobs clear
	if len(existingJobs.Items) > 1 {
		err := fmt.Errorf("expecting zero or one log collector job per event per node, found %v", len(existingJobs.Items))
		tracing.RecordError(span, err)
		span.SetAttributes(attribute.String("fault_remediation.error.type", "multiple_log_collector_jobs_error"))
		span.SetAttributes(attribute.String("fault_remediation.error.message", err.Error()))

		return batchv1.Job{}, ctrl.Result{}, err
	}

	if len(existingJobs.Items) == 0 {
		err = c.client.Create(ctx, job)
		if err != nil {
			tracing.RecordError(span, err)
			span.SetAttributes(attribute.String("fault_remediation.error.type", "create_log_collector_job_error"))
			span.SetAttributes(attribute.String("fault_remediation.error.message", err.Error()))

			return batchv1.Job{}, ctrl.Result{}, err
		}
		// if created, requeue to check status later
		return batchv1.Job{}, ctrl.Result{RequeueAfter: controllerRequeueDuration}, nil
	}

	return existingJobs.Items[0], ctrl.Result{}, nil
}

func (c *FaultRemediationClient) checkLogCollectorStatus(
	ctx context.Context,
	nodeName string,
	job batchv1.Job,
) (ctrl.Result, error) {
	// convert to metav1 condition
	conditions := make([]metav1.Condition, len(job.Status.Conditions))
	for i, jc := range job.Status.Conditions {
		conditions[i] = metav1.Condition{
			Type:   string(jc.Type),
			Status: metav1.ConditionStatus(jc.Status),
			Reason: jc.Reason,
		}
	}

	found, err := c.checkLogCollectorComplete(ctx, nodeName, job, conditions)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check log collector status: %w", err)
	}

	if found {
		return ctrl.Result{}, nil
	}

	found, err = c.checkLogCollectorFailed(ctx, nodeName, job, conditions)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check log collector status: %w", err)
	}

	if found {
		return ctrl.Result{}, nil
	}

	found, err = c.checkLogCollectorTimedOut(ctx, nodeName, job)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check log collector status: %w", err)
	}

	if found {
		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: controllerRequeueDuration}, nil
}

func (c *FaultRemediationClient) checkLogCollectorComplete(
	ctx context.Context,
	nodeName string,
	job batchv1.Job,
	conditions []metav1.Condition,
) (bool, error) {
	completeCondition := meta.FindStatusCondition(conditions, string(batchv1.JobComplete))
	if completeCondition == nil || completeCondition.Status != metav1.ConditionTrue {
		return false, nil
	}

	slog.InfoContext(ctx, "Log collector job completed successfully", "job", job.Name)
	// Use job's actual duration instead of custom tracking
	// reconciliation can be called multiple times so use annotation to make sure we're not duplicate recording metrics
	if job.Annotations == nil || job.Annotations[jobMetricsAlreadyCountedAnnotation] != trueStringVal {
		updateJob := job.DeepCopy()
		if updateJob.Annotations == nil {
			updateJob.Annotations = map[string]string{}
		}

		updateJob.Annotations[jobMetricsAlreadyCountedAnnotation] = trueStringVal

		err := c.client.Update(ctx, updateJob)
		if err != nil {
			return false, err
		}

		metrics.LogCollectorJobs.WithLabelValues(nodeName, "success").Inc()

		if job.Status.StartTime != nil {
			duration := job.Status.CompletionTime.Sub(job.Status.StartTime.Time).Seconds()
			metrics.LogCollectorJobDuration.WithLabelValues(nodeName, "success").Observe(duration)

			span := tracing.SpanFromContext(ctx)
			span.SetAttributes(
				attribute.String("fault_remediation.log_collector.job_name", job.Name),
				attribute.String("fault_remediation.log_collector.result", "success"),
				attribute.Float64("fault_remediation.log_collector.duration_seconds", duration),
			)
		}
	}

	return true, nil
}

func (c *FaultRemediationClient) checkLogCollectorFailed(
	ctx context.Context,
	nodeName string,
	job batchv1.Job,
	conditions []metav1.Condition,
) (bool, error) {
	// check if failed
	failedCondition := meta.FindStatusCondition(conditions, string(batchv1.JobFailed))
	if failedCondition == nil || failedCondition.Status != metav1.ConditionTrue {
		return false, nil
	}

	slog.InfoContext(ctx, "Log collector job failed", "job", job.Name)

	// reconciliation can be called multiple times so use annotation to make sure we're not duplicate recording metrics
	if job.Annotations == nil || job.Annotations[jobMetricsAlreadyCountedAnnotation] != trueStringVal {
		if err := c.recordLogCollectorFailureMetrics(ctx, job, nodeName); err != nil {
			return false, err
		}
	}

	// dont return error so reconciliation can continue
	return true, nil
}

// recordLogCollectorFailureMetrics updates the job annotation and records failure metrics.
func (c *FaultRemediationClient) recordLogCollectorFailureMetrics(
	ctx context.Context,
	job batchv1.Job,
	nodeName string,
) error {
	updateJob := job.DeepCopy()
	if updateJob.Annotations == nil {
		updateJob.Annotations = map[string]string{}
	}

	updateJob.Annotations[jobMetricsAlreadyCountedAnnotation] = trueStringVal

	if err := c.client.Update(ctx, updateJob); err != nil {
		return err
	}

	var duration float64

	switch {
	case job.Status.StartTime == nil:
		duration = 0
	case job.Status.CompletionTime != nil:
		duration = job.Status.CompletionTime.Sub(job.Status.StartTime.Time).Seconds()
	default:
		duration = time.Since(job.Status.StartTime.Time).Seconds()
	}

	metrics.LogCollectorJobs.WithLabelValues(nodeName, "failure").Inc()
	metrics.LogCollectorJobDuration.WithLabelValues(nodeName, "failure").Observe(duration)

	span := tracing.SpanFromContext(ctx)
	span.SetAttributes(
		attribute.String("fault_remediation.log_collector.job_name", job.Name),
		attribute.String("fault_remediation.log_collector.outcome", "failure"),
		attribute.Float64("fault_remediation.log_collector.duration_seconds", duration),
	)

	return nil
}

func (c *FaultRemediationClient) checkLogCollectorTimedOut(
	ctx context.Context,
	nodeName string,
	job batchv1.Job,
) (bool, error) {
	timeout := 10 * time.Minute // Default timeout: 10 minutes

	if timeoutEnv := os.Getenv("LOG_COLLECTOR_TIMEOUT"); timeoutEnv != "" {
		if parsed, err := time.ParseDuration(timeoutEnv); err == nil {
			timeout = parsed
		} else {
			slog.WarnContext(ctx, "Invalid LOG_COLLECTOR_TIMEOUT value, using default 10m",
				"timeout-value", timeoutEnv, "error", err)
		}
	}

	// check timeout
	if time.Since(job.CreationTimestamp.Time) <= timeout {
		return false, nil
	}

	slog.InfoContext(ctx, "Log collector job past timeout", "job", job.Name, "timeout", timeout)

	if job.Annotations == nil || job.Annotations[jobMetricsAlreadyCountedAnnotation] != trueStringVal {
		updateJob := job.DeepCopy()
		if updateJob.Annotations == nil {
			updateJob.Annotations = map[string]string{}
		}

		updateJob.Annotations[jobMetricsAlreadyCountedAnnotation] = trueStringVal

		err := c.client.Update(ctx, updateJob)
		if err != nil {
			return false, err
		}

		metrics.LogCollectorJobs.WithLabelValues(nodeName, "timeout").Inc()
		metrics.LogCollectorJobDuration.WithLabelValues(nodeName, "timeout").Observe(timeout.Seconds())

		span := tracing.SpanFromContext(ctx)
		span.SetAttributes(
			attribute.String("fault_remediation.log_collector.job_name", job.Name),
			attribute.String("fault_remediation.log_collector.result", "timeout"),
			attribute.Float64("fault_remediation.log_collector.duration_seconds", timeout.Seconds()),
		)
	}

	return true, nil
}

func (c *FaultRemediationClient) GetConfig() *config.TomlConfig {
	return &c.remediationConfig
}
