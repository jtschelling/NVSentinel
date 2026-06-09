# VHS tape: ExternalRemediationRequest happy-path demo

Output docs/demos/err-happy-path.gif
Set Theme "Catppuccin Mocha"
Set FontSize 14
Set Width 1200
Set Height 650
Set Shell "zsh"
Set Padding 16
Set PlaybackSpeed 1

# Bootstrap: cd, source helpers, ensure clean state (hidden from recording)
Hide
Type "cd /Users/jschelling/git/gh/NVIDIA/NVSentinel && source docs/demos/err-demo-helpers.sh"
Enter
Sleep 2s
Type "kubectl delete err $ERR_NAME $NS --ignore-not-found && sleep 6"
Enter
Sleep 8s
Show
Ctrl+L
Sleep 500ms

# ── 1. Baseline ──────────────────────────────────────────────────────────────
Type "echo '── 1. Baseline: kwok-node-5 — no release taint, no managed label ──'"
Enter
Sleep 500ms
Type "err_baseline"
Enter
Sleep 3s

# ── 2. Show manifest ─────────────────────────────────────────────────────────
Type "echo '── 2. The ExternalRemediationRequest manifest ──'"
Enter
Sleep 500ms
Type "cat docs/demos/err-demo-node.yaml"
Enter
Sleep 3s

# ── 3. Apply ─────────────────────────────────────────────────────────────────
Ctrl+L
Type "echo '── 3. Apply ERR (fault-remediation does this automatically) ──'"
Enter
Sleep 500ms
Type "err_apply"
Enter
Sleep 5s

# ── 4. Reconciler fires ───────────────────────────────────────────────────────
Ctrl+L
Type "echo '── 4. Janitor ERR reconciler: release taint + managed=false applied ──'"
Enter
Sleep 500ms
Type "err_node_state"
Enter
Sleep 3s

# ── 5. ERR conditions ────────────────────────────────────────────────────────
Type "echo '── 5. ERR conditions: ownership released, awaiting external system ──'"
Enter
Sleep 500ms
Type "err_conditions"
Enter
Sleep 3s

# ── 6. Janitor logs ───────────────────────────────────────────────────────────
Type "echo '── 6. Janitor apply-path log ──'"
Enter
Sleep 500ms
Type "err_logs"
Enter
Sleep 3s

# ── 7. External system signals completion ────────────────────────────────────
Ctrl+L
Type "echo '── 7. External system patches ExternalRemediationComplete=True ──'"
Enter
Sleep 500ms
Type "err_complete"
Enter
Sleep 8s

# ── 8. Node restored ─────────────────────────────────────────────────────────
Ctrl+L
Type "echo '── 8. Cleanup: release taint removed, managed label cleared ──'"
Enter
Sleep 500ms
Type "err_node_state"
Enter
Sleep 3s

# ── 9. ERR finalized ─────────────────────────────────────────────────────────
Type "echo '── 9. ERR finalizer complete — object deleted ──'"
Enter
Sleep 500ms
Type "err_list"
Enter
Sleep 4s

Ctrl+L
Type "echo '✓ Node restored. ERR lifecycle complete.'"
Enter
Sleep 3s
