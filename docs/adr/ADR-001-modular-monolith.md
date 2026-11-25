# ADR-001: Use a Modular Monolith

- **Status:** Superseded for runtime topology by [ADR-020](ADR-020-go-backend.md); domain modularity remains accepted
- **Date:** 2026-08-24
- **Decision owners:** Product Architect / Technical Lead
- **Applies to:** V1 architecture

## Context

Better Content V1 contains several distinct domains: authentication, workspaces, Content DNA, idea generation, content editing, publishing workflow, social integrations, analytics, and AI integration.

These domains need clear boundaries, but V1 does not currently require independent deployment, independent scaling, or separate operational ownership.

Introducing microservices now would add deployment complexity, distributed transactions, network failure modes, service authentication, observability overhead, and duplicated infrastructure before those costs are justified.

## Decision

Better Content V1 will be implemented as a **modular monolith** in a single Next.js application.

The application will contain explicit internal modules for major domains, including:

- workspace
- DNA
- ideas
- content
- publishing
- integrations
- analytics
- AI
- assets
- jobs

Modules should interact through application services and explicit domain interfaces rather than arbitrary cross-module imports.

PostgreSQL is the shared system of record.

## Consequences

### Positive

- Faster V1 development.
- Simpler deployment and local development.
- Easier transactional consistency.
- Easier tracing across idea → content → publication → analytics.
- Lower operational burden.
- Clear future extraction points if a module later needs independent deployment.

### Negative

- Module boundaries are enforced by code organization and engineering discipline rather than network boundaries.
- A poorly structured monolith could become tightly coupled if the dependency rules are ignored.

## Rejected alternatives

### Microservices from V1

Rejected because V1 does not justify the operational complexity.

### Separate backend API service

Rejected because Next.js server functionality can support the required V1 application and API boundaries without a separate backend deployment.

## Constraints

Codex must not introduce a separate service, microservice framework, message broker, or distributed architecture without a new ADR.

## Supersession

ADR-020 supersedes the single-Next.js-runtime decision for the approved Go
backend migration. The bounded modular-monolith structure, explicit domain
boundaries, PostgreSQL system of record, and rejection of per-domain
microservices remain in force.
