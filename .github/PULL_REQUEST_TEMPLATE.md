## Summary

-

## Test plan

- [ ] `make test-fast` for this change surface
- [ ] `make test` locally when API compatibility, Harbor verifiers, packaging, or docs claims changed; the merge queue runs it once on the combined SHA
- [ ] `make docs-qa` when docs/README/claims changed
- [ ] Provider-backed Harbor, Inspect, or live API compatibility only for a manual promotion
- [ ] No secrets (`env.json`, `commerce.env.json`, tokens, licenses) added
- [ ] Commits include `Signed-off-by`

## Notes

-
