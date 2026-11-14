# Proof of concept: native promotions from GitLab

A small, separate path that promotes through PingOne's native Configuration Management API and shows, field by field, what each promotion changes in the target. It does not replace the hosted server; it exists to decide whether the native API can.

## Try it without credentials

```sh
go run ./cmd/p1-native-poc demo --out demo
open demo/1-plan/report.html      # the plan a reviewer reads
cat demo/1-plan/mr-note.md        # the same plan as a merge request note
open demo/2-rollback/report.html  # the rollback of that promotion
```

The demo runs against `internal/nativepoc/mockapi`, an in-memory stand-in with two invented environments. It plans a promotion, shows that an apply is refused after someone edits the target by hand, applies it, and plans the rollback.

## How it works

1. A person adds `promotions/<ticket>.yml` in a merge request: source, target and the items by type and name.
2. `plan` snapshots the source, drafts the native promotion (PingOne adds what the selection depends on) and reads its operations. For each operation it compares the new configuration with what the target has now and writes:
   - `report.html`, the full review;
   - `mr-note.md`, the same review as a merge request note;
   - `plan.json`, the review plus two digests: the promotion's operations, and what the reviewer was shown.
3. `apply` reads the operations again and takes a fresh snapshot of the target. It runs the promotion only if both digests still match; otherwise it says to plan again.
4. `rollback` drafts PingOne's rollback of that promotion as a plan of its own, reviewed and applied the same way.

What a reviewer sees:
- every item as create, update or delete, with the items PingOne added as dependencies marked;
- for an update, each field with its value now and after. An element added to a list (a node in a flow) is one line, and the elements after it are not reported as changed;
- secrets never shown (by key name and by value, with the rules the hosted server uses);
- "look at these first": a dependency that will be updated though nobody selected it, and a new value that names the source environment (a URL with `dev` in its host), which usually needs a promotion variable.

The example pipeline is `examples/native-pipeline/.gitlab-ci.yml`.

## What is verified and what is assumed

- **Verified against the mock only.** Nothing here has called the live API. The request and response shapes are those the earlier native client of this repository used against the live service: `POST /snapshots?expand=all`, `/snapshots/{env}/versions/{id}/embeddedSnapshots`, `POST /promotions`, `/promotions/{id}/promotionSteps` (`action`, `resourceUrl`, `sourceResource`, `targetResource`, `payload`), and the execute and rollback media types. They are all in `internal/nativepoc/api.go`.
- **To check in a sandbox first:**
  1. the promotion and rollback calls that returned HTTP 500 and 400 before;
  2. that a step's `payload` is the full new configuration (the review compares it with the target's snapshot), including for DaVinci flows;
  3. how promotion variables appear in a step, so a value kept per environment is shown as kept;
  4. how large a real flow's snapshot is, and whether the note needs a tighter summary than the report.
- **Not built:** promotion variables, resource mapping, more than one promotion per merge request, the Requests page, and any link to the hosted server.

## What Git is in this design

Git holds the request, the reviewed plan and the approval. The configuration itself lives in PingOne's snapshots. A change made by hand in the target is not in Git; the next plan shows it, and an apply after it is refused.
