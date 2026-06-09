# VHS tape: ExternalRemediationRequest happy-path demo
# Re-render: vhs docs/demos/err-happy-path.tape (requires vhs, ttyd, ffmpeg)

Output docs/demos/err-happy-path.gif
Set Theme "Catppuccin Mocha"
Set FontSize 14
Set Width 1200
Set Height 650
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
Sleep 500ms

# 1. Baseline — no release taint, no managed label
Type "kubectl get node kwok-node-5 -o yaml | grep -E 'external-remediation|managed: '"
Enter
Sleep 5s

# 2. Show manifest
Type "cat docs/demos/err-demo-node.yaml"
Enter
Sleep 5s

# 3. Apply
Sleep 5s
Ctrl+L
Type "kubectl apply -f docs/demos/err-demo-node.yaml"
Enter
Sleep 6s

# 4. Reconciler applied release taint + managed=false
Ctrl+L
Type "kubectl get node kwok-node-5 -o yaml | grep -E 'external-remediation|managed: '"
Enter
Sleep 5s

# 5. ERR conditions
Type "kubectl get err err-kwok-node-5-a1b2c3d4 -n nvsentinel -o yaml | grep -A2 'type:.*Remediation'"
Enter
Sleep 5s

# 6. Janitor log
Type "kubectl logs -n nvsentinel deploy/janitor --tail=30 | grep node-5 | grep -v WARN | tail -2 | jq -r .msg"
Enter
Sleep 5s

# 7. External system patches Complete=True
Sleep 5s
Ctrl+L
Type "kubectl patch err err-kwok-node-5-a1b2c3d4 -n nvsentinel --type merge --subresource status --patch-file docs/demos/complete-patch.json"
Enter
Sleep 9s

# 8. Cleanup — taint and managed label removed
Sleep 5s
Ctrl+L
Type "kubectl get node kwok-node-5 -o yaml | grep -E 'external-remediation|managed: '"
Enter
Sleep 5s

# 9. ERR gone
Type "kubectl get externalremediationrequests -n nvsentinel"
Enter
Sleep 5s
