# TODO

- [ ] **Decide whether `Suspended` health counts as done** for a rollout step. Today only `Healthy`
  does, so an app with a suspended CronJob waits the full health timeout and then fails.
- [ ] **Test a full rollout on real clusters.** So far the rollout engine has only run against a
  fake ArgoCD.
- [ ] **Keep watch streams open behind short proxy timeouts.** A proxy in front of ArgoCD may end
  `/api/v1/stream/applications` after, for example, 30 seconds. argo-center resumes from the last
  seen resourceVersion, but a change that lands in the gap is only picked up by the next re-list
  (`resyncInterval`, 5 minutes by default). The fix is on the proxy: allow long responses on
  `/api/v1/stream/`.
