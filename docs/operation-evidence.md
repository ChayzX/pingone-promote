# Execution evidence and compatibility

Native status, dispatch evidence and verified resource effects are separate facts.
`COMPLETED` reports a native outcome; it does not establish which resources
changed or whether excluded resources remained untouched.

The app's optional `execution` history metadata has version `1`:

- `dispatchState: uncertain` is saved with execution intent before the POST.
  It means the request might have been sent, including cases where no response
  was received. It is not proof of acceptance.
- `dispatchState: identified` is checkpointed after a response establishes the
  expected native identity, before further polling. A checkpoint failure retains
  the earlier uncertain intent and stops polling.
- `nativeObserved` records observation separately. A later read can observe
  completion without proving that the earlier uncertain request was accepted.
- `effectsVerified` stays false. Local metadata alone cannot establish resource
  verification; the history response also suppresses disk-only claims of it.

The plan response additionally projects `needsReconciliation` and
`mayHaveEffects` through the shared lifecycle model. Unrecognized native status
is presented as `UNKNOWN`; arbitrary native status text is not operator progress.
History inspection projects old unknown status safely without rewriting the
retained record. App-owned creation progress states remain visible.

Existing records without `execution` remain readable. A retained legacy intent
is treated conservatively as uncertain, and the existing intent still prevents
replay. Existing reviews without an execution attempt retain their normal gates.
Unsupported versions or inconsistent execution metadata block deployment and
rollback requests; preserve the evidence and investigate rather than editing it
to bypass the block. New rollback children do not inherit the forward operation's
execution evidence.

These are additive metadata and presentation changes. They do not migrate an
approval or change artifact review digests. Production planning, execution and
rollback remain restricted to GitLab. The runner's private execution sidecar and
its host-loss limitations are described in [GitLab operations](gitlab.md).

This evidence does not implement resource-effect verification or certify live
recovery. A separately reviewed rollback and before/after resource evidence are
still required for live acceptance.
