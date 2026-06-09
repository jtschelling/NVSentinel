#!/usr/bin/env bash
# Helper functions for the ERR demo tape — sourced at the start of the session.

export PATH="$HOME/.local/share/mise/installs/kubectl/1.31.14/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"
export KUBECONFIG="$HOME/.kube/config"
kubectl config use-context kind-nvsentinel-dev >/dev/null 2>&1

ERR_NAME="err-kwok-node-5-a1b2c3d4"
NODE="kwok-node-5"
NS="-n nvsentinel"

err_baseline() {
  kubectl get node $NODE \
    -o custom-columns='TAINTS:.spec.taints[*].key,MANAGED:.metadata.labels.nvsentinel\.dgxc\.nvidia\.com/managed'
}

err_apply() {
  kubectl apply -f docs/demos/err-demo-node.yaml
}

err_list() {
  kubectl get externalremediationrequests $NS
}

err_node_state() {
  local taint
  taint=$(kubectl get node $NODE -o jsonpath='{.spec.taints[1].key}={.spec.taints[1].value}')
  local managed
  managed=$(kubectl get node $NODE -o jsonpath='{.metadata.labels.nvsentinel\.dgxc\.nvidia\.com/managed}')
  echo "Release taint : $taint"
  echo "Managed label : ${managed:-<absent>}"
}

err_conditions() {
  kubectl get err $ERR_NAME $NS \
    -o jsonpath='{range .status.conditions[*]}{.type}={.status}  ({.reason}){"\n"}{end}'
}

err_logs() {
  kubectl logs $NS deploy/janitor --tail=40 \
    | grep "kwok-node-5" | grep -v WARN \
    | tail -3 \
    | jq -r '"\(.msg) | node=\(.node)"'
}

err_complete() {
  kubectl patch err $ERR_NAME $NS \
    --type=merge --subresource=status \
    -p '{"status":{"conditions":[{"type":"ExternalRemediationComplete","status":"True","reason":"RemediationSucceeded","message":"Node is healthy.","lastTransitionTime":"2026-06-08T23:30:00Z"}]}}'
}
