# Agent Instructions

## Architecture

Read `docs/adr/README.md` and relevant accepted ADRs before changing architecture,
deployment, identity, secrets, storage, or capability boundaries.

Record decisions with lasting consequences as ADRs. Keep proposed decisions
clearly distinguishable from accepted ones.

## Secrets

- Never read, decrypt, print, log, or commit plaintext secrets.
- `direnv` is an environment-loading convenience, not an agent sandbox.
- Do not give the workspace decryption identities or broad static credentials.
- Prefer short-lived, narrowly scoped workload identities and external approval
  boundaries.
- Never add secret material to images, build contexts, Kubernetes manifests,
  persistent workspace state, test fixtures, or AI instructions.

## Deployment

- Kubernetes resources are the canonical local and remote deployment contract.
- Do not add a parallel Docker Compose deployment.
- Local Kubernetes may use Minikube or another conforming implementation.
- Build and deploy the same OCI image locally and remotely.
- A T3 workspace has exactly one active T3 server and writer for its state.

## Git

- GitHub (`alcxyz/paw`) is canonical for code, issues, PRs, CI, and releases.
  Forgejo (`alcxyz/paw`) is a public read-only pull mirror of GitHub, refreshed
  every eight hours; do not push to it or open PRs there. Pre-mirror Forgejo-only
  branches are retained in `alcxyz/paw-continuity-archive-20260923`.
- Use `dev` for normal development and promote to protected `main` through a
  GitHub pull request.
- Do not create or publish an external repository without confirming its host,
  organization, name, and visibility.
