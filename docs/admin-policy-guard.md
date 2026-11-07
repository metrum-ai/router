<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Private policy-mutation guard

This core does not expose a mutation endpoint or establish transactional safety for existing writes.

The actor must exactly match the authenticated context principal. Real actor reauthorization retains the durable-audit failure behavior. Synthetic ceiling and last-admin checks use the private, source-gated `simulatePolicyGuard` entry with isolated principal/snapshot context and a no-op audit callback; they must never write to the real audit sink.

`security-admin` is a reserved role name, not a permissible principal subject or credential subject mapping. Membership requires a grouping row. Built-in role capability rows cannot be acquired by mapping an authenticated principal to the role's own name. The entire `router-internal:` prefix is also reserved.

## Bounded candidates

The guard rejects candidates exceeding any of:

- 256 authored records, counting duplicate/unchanged records;
- 64 authored role memberships, also counting duplicates;
- 1,024 compiled policy rows, including immutable/derived compatibility rows;
- 256 compiled role memberships, including generated bindings.

Over-limit candidates return `invalid-policy` before role-bundle simulations. Consumers must enforce submitted limits before constructing the candidate as well. These are mutation limits; existing legacy serving-policy loading is not changed.

Literal action alternatives are checked individually: a read-only actor cannot delegate `read|write`. A general regex grant requires the **identical existing regex pattern**, with structurally sufficient domain/object coverage and applicable role membership. The guard does not infer arbitrary regex-language containment from sampling or a broader-looking expression.

## Transaction-wiring gate

The consuming mutation path must hold **one mutation lock across current authorization, candidate authorization and enabled-principal inventory snapshots**, and retain that authority through decision/persistence. Request pins are not mutation authority. Successful mutation and audit must commit in the same governing transaction; rejected attempts must be recorded durably after rollback. Emergency authority must not count as a normal last administrator.

A process mutex or policy-store-only row lock does not prove safety when authentication/config and policy stores differ. The cross-store authority decision, full lifecycle ceilings, durable audit integration and freshness remain separate #812/#813 gates.
