# What I learned building a toy multi-tenant control plane in Go

*Draft by Raef Hany. This is a learning project. It is not production software, it is not affiliated with Sourcegraph, and it contains no Sourcegraph code.*

I have spent about four years running Kubernetes, Terraform and ArgoCD for customers. What I had not done is write a Kubernetes operator, drive Terraform from code with CDK for Terraform, or think about a fleet of single-tenant environments as one thing to be managed. So I built a small one to find out where my mental model was wrong.

The shape is inspired by public material: Robert Lin's 2024 post on scaling Sourcegraph's managed multi-single-tenant product (I could not find a live URL to link, so I cite it by title only), Sourcegraph's post [Building Sourcegraph Cloud as a single-tenant solution](https://sourcegraph.com/blog/single-tenant-cloud), and the public [Sourcegraph Cloud docs](https://sourcegraph.com/docs/cloud) on private connectivity. I read them for ideas ("each customer is a Kubernetes custom resource", "tasks dispatched per instance") and wrote my own code. Nothing here is how Sourcegraph actually works internally, and nothing here has run at any scale. The repo is [Raef-Hany/fleet-operator](https://github.com/Raef-Hany/fleet-operator).

## The shape

One cluster-scoped `Instance` resource per tenant. The controller turns it into a namespace with a quota, network policies, and a podinfo Deployment (podinfo because it has real versions and health endpoints, so an upgrade is observable). It also dispatches a Kubernetes Job that renders Terraform for the tenant and runs `terraform validate`. Nothing is ever applied, and the project never needs a cloud account.

## Idempotency is a design decision, not a property

My first instinct was to `Create` objects and fall back to `Update`. That works until two things write to the same object. I switched to server-side apply with a fixed field owner, which gives you drift correction almost for free: delete the Deployment or edit its replicas and the next reconcile puts it back.

The part I underestimated was *steady state*. A reconcile that has nothing to do should do nothing. Server-side apply of an unchanged object does not change etcd, but it is still an API call, and a controller that patches everything on every reconcile makes it hard to reason about what it is doing. So the reconciler first does a dry-run apply and compares the result to the live object, and only issues the real apply if they differ. One of the tests counts the writes a steady-state reconcile makes, using a client interceptor, and asserts the count is zero. The status update follows the same rule: compare, and write only if it changed. Writing status unconditionally is a classic way to get a hot loop, because the write itself triggers another reconcile.

## Finalizers made me think about time

A finalizer sounds like "run some cleanup on delete". In practice it is a promise that the object will not disappear until you say so, which means your cleanup has to be re-entrant. On delete, my reconciler asks for the namespace to be deleted, notices that it is still `Terminating`, requeues, and only removes the finalizer once the namespace is really gone. Each reconcile might be the first or the fifth, so every step has to be safe to repeat.

It also taught me what the tests could not tell me. In envtest nothing finishes terminating a namespace, so the test has to play the namespace controller: it clears the namespace's spec finalizers through the `finalize` subresource and then checks that the operator releases its own finalizer. That is honest about what is being tested: my reconciler's logic, not Kubernetes' namespace lifecycle.

## Why Terraform runs as a Job

CDK for Terraform in Go works through jsii, which means Node has to be present when you synthesize. I did not want Node inside the manager. So synthesis lives in a separate `tfrender` image and runs as a Job per instance. The manager stays a small static Go binary, and the Job is the unit of "a task for this instance".

The Job name is `tf-<tenant>-<hash8>`, where the hash covers only the inputs that affect the Terraform: tenant, region, ring and connectivity. Changing the version or size does not create a new Job; changing the region does. Because the name is deterministic, creating it is idempotent: "already exists" counts as success, and an old hash's Job is never recreated. I also split the hash function into its own package so the manager does not link the CDKTF dependency tree just to compute a string. I checked that with `go list -deps`.

A failed Job is a terminal `Failed` phase with the Job's message in a condition. Recovery is a spec change, which produces a new hash and a new Job. That is a simplification I would revisit.

## What envtest cannot test

envtest runs a real API server and etcd, but no controllers. No Deployment ever becomes available, no Job ever runs, no namespace ever finishes terminating, and nothing is garbage collected. My reconciler tests set those statuses by hand. That is fine for proving how the reconciler reacts to them, and wrong as evidence that a rollout works. For that I have a kind end-to-end test: three instances in different rings reach `Ready`, one is upgraded and the new image rolls out, one is deleted and its namespace goes away. It is the only place where the pieces really run together.

The practical lesson is to decide up front which layer is responsible for which claim, and not let green unit tests stand in for a real cluster.

## Other things that bit me

- The pure rendering functions (`Render(instance) []client.Object`, and the Terraform synthesis) turned out to be where most of the value is. Golden files made review easy, and none of it needs a cluster.
- Some Google resources in the Terraform provider have no `labels` field, so "label everything" is not literally possible. The labels go where the schema supports them and the facts go into descriptions elsewhere.
- Short tenant names and long tenant names both break naive service account ID rules. Truncating a long name made two different tenants collide; a test caught it and the fix appends a short hash.
- Kubernetes validates Job status updates, so a test cannot just set `Complete=True`. I had to read the validation errors to learn what a legal completed Job looks like.
- On Windows, controller-runtime's envtest did not even compile, so the tests run in a Linux container. Expect some tooling friction if you are on Windows.

## Honest limits

This is a few thousand lines written to learn from. The workload is podinfo. There is no persistence, no ingress, no real tenant data path, and no multi-cluster. The Terraform has only ever been synthesized and validated, never applied against a real project. `privatelink` is a labelled placeholder that renders an output saying it is not implemented. CDKTF itself has been announced as deprecated by HashiCorp, so I would not start a new system on it; I used it because I wanted to understand how it behaves. I also did not build the ring-based rollout controller I sketched; the `ring` field exists but nothing acts on it yet.

If you are about to write your first operator, my advice is short. Make every step re-runnable. Compare before you write. Be explicit about which test layer proves which claim. And keep the rendering code pure, because it is the part you will want to change most.
