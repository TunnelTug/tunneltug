# Never kubectl for TunnelTug fleets

TunnelTug owns k3s reconcile. **Never use kubectl** to deploy, scale, patch, or “fix” barges/stacks TunnelTug manages. Use `-mode stack` / `-mode barge -barge-runtime k3s`, hub-publish, and unit restarts only.
