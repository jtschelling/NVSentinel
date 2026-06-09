#!/usr/bin/env bash
# Helper functions for the ERR demo tape — sourced at the start of the session.

export PATH="$HOME/.local/share/mise/installs/kubectl/1.31.14/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"
export KUBECONFIG="$HOME/.kube/config"
kubectl config use-context kind-nvsentinel-dev >/dev/null 2>&1

err_baseline() {
  kubectl get node kwok-node-5 \
    -o custom-columns='TAINTS:.spec.taints[*].key,MANAGED:.metadata.labels.nvsentinel\.dgxc\.nvidia\.com/managed'
}

err_apply() {
  kubectl apply -f docs/demos/err-demo-node.yaml
}

err_node_state() {
  local taint managed
  taint=$(kubectl get node kwok-node-5 -o jsonpath='{.spec.taints[1].key}={.spec.taints[1].value}')
  managed=$(kubectl get node kwok-node-5 -o jsonpath='{.metadata.labels.nvsentinel\.dgxc\.nvidia\.com/managed}')
  echo "Release taint : $taint"
  echo "Managed label : ${managed:-<absent>}"
}

err_conditions() {
  kubectl get err err-kwok-node-5-a1b2c3d4 -n nvsentinel \
    -o jsonpath='{range .status.conditions[*]}{.type}={.status}  ({.reason}){"\n"}{end}'
}

err_logs() {
  kubectl logs -n nvsentinel deploy/janitor --tail=40 \
    | grep "kwok-node-5" | grep -v WARN \
    | tail -3 \
    | jq -r '"\(.msg) | node=\(.node)"'
}

err_complete() {
  kubectl patch err err-kwok-node-5-a1b2c3d4 -n nvsentinel \
    --type=merge --subresource=status \
    -p '{"status":{"conditions":[{"type":"ExternalRemediationComplete","status":"True","reason":"RemediationSucceeded","message":"Node is healthy.","lastTransitionTime":"2026-06-08T23:30:00Z"}]}}'
}

err_cleanup() {
  kubectl delete err err-kwok-node-5-a1b2c3d4 -n nvsentinel --ignore-not-found
}
