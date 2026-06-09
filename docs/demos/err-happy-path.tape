# VHS tape: ExternalRemediationRequest happy-path demo
# Re-render: vhs docs/demos/err-happy-path.tape (requires vhs, ttyd, ffmpeg)

Output docs/demos/err-happy-path.gif
Set Theme "Catppuccin Mocha"
Set FontSize 14
Set Width 1200
Set Height 700
Set Shell "zsh"
Set Padding 16
Set PlaybackSpeed 1

Hide
Type "cd /Users/jschelling/git/gh/NVIDIA/NVSentinel"
Enter
Type "export PATH=/Users/jschelling/.local/share/mise/installs/kubectl/1.31.14/bin:/opt/homebrew/bin:$PATH"
Enter
Type "export KUBECONFIG=$HOME/.kube/config"
Enter
Type "kubectl config use-context kind-nvsentinel-dev"
Enter
Type "kubectl delete err err-kwok-node-5-a1b2c3d4 -n nvsentinel --ignore-not-found"
Enter
Sleep 8s
Show
Ctrl+L

# 1. Baseline
Type "echo '── 1. Baseline: kwok-node-5 has only the standard GPU taint ──'"
Enter
Sleep 500ms
Type "kubectl get node kwok-node-5 -o yaml | yq '.spec.taints'"
Enter
Sleep 5s

# 2. ERR manifest
Type "echo '── 2. The ExternalRemediationRequest manifest ──'"
Enter
Sleep 500ms
Type "cat docs/demos/err-demo-node.yaml"
Enter
Sleep 5s

# 3. Apply
Sleep 5s
Ctrl+L
Type "echo '── 3. fault-remediation creates the ERR on EXTERNAL_REMEDIATION action ──'"
Enter
Sleep 500ms
Type "kubectl apply -f docs/demos/err-demo-node.yaml"
Enter
Sleep 6s

# 4. Reconciler result
Sleep 5s
Ctrl+L
Type "echo '── 4. janitor ERR reconciler: release taint + managed=false applied ──'"
Enter
Sleep 500ms
Type "kubectl get node kwok-node-5 -o yaml | yq '.spec.taints'"
Enter
Sleep 4s
Type "kubectl get node kwok-node-5 -o yaml | yq '.metadata.labels'"
Enter
Sleep 5s

# 5. ERR conditions
Sleep 5s
Ctrl+L
Type "echo '── 5. ERR conditions: NVSentinel released, awaiting external system ──'"
Enter
Sleep 500ms
Type "kubectl get err err-kwok-node-5-a1b2c3d4 -n nvsentinel -o yaml | yq '.status.conditions'"
Enter
Sleep 5s

# 6. Patch — show it first, then apply
Sleep 5s
Ctrl+L
Type "echo '── 6. external system signals Complete=True ──'"
Enter
Sleep 500ms
Type "cat docs/demos/complete-patch.json | yq"
Enter
Sleep 4s
Type "kubectl patch err err-kwok-node-5-a1b2c3d4 -n nvsentinel --type merge --subresource status --patch-file docs/demos/complete-patch.json"
Enter
Sleep 9s

# 7. Node restored
Sleep 5s
Ctrl+L
Type "echo '── 7. reconciler cleanup: taint removed, managed label cleared ──'"
Enter
Sleep 500ms
Type "kubectl get node kwok-node-5 -o yaml | yq '.spec.taints'"
Enter
Sleep 4s
Type "kubectl get node kwok-node-5 -o yaml | yq '.metadata.labels'"
Enter
Sleep 5s

# 8. ERR finalized
Type "echo '── 8. ERR finalizer ran — object deleted ──'"
Enter
Sleep 500ms
Type "kubectl get externalremediationrequests -n nvsentinel"
Enter
Sleep 5s
