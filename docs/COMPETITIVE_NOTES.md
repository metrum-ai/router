# Competitive Notes

These notes support public competitive docs. Keep public claims primary-source-first and source-dated. Do not copy exact pricing into public docs unless it was revalidated during that task.

Last reviewed: 2026-09-19.

**Website follow-up:** the www.metrum.ai `/router` capability matrix is outside this repository. Track the public-site update in [issue #211](https://github.com/metrum-ai/router/issues/211). Source-only handoff notes live in `docs/marketing-copy-handoff.md`.

## Public Positioning Rule

Position Metrum AI Router as a governed enterprise LLM smart router with deployment-defined routing control, detailed usage/cost accounting, private upstream support, and agent-client compatibility. Gateway functions are subordinate to routing.

Do not position it as:

- a public model marketplace;
- a generic observability SaaS;
- a full enterprise admin suite with SSO/session management and compliance workflow automation; the repository includes a focused authenticated browser reporting surface for usage/performance/cost operations;
- an automatic quality oracle for every model/prompt.

## Primary Source Baseline

Use these primary links for public docs:

- LiteLLM Enterprise: https://docs.litellm.ai/docs/enterprise
- Bifrost overview: https://docs.getbifrost.ai/overview
- Bifrost GitHub: https://github.com/maximhq/bifrost
- OpenRouter pricing: https://openrouter.ai/pricing
- Portkey pricing: https://portkey.ai/pricing
- Helicone pricing: https://www.helicone.ai/pricing
- Cloudflare AI Gateway pricing: https://developers.cloudflare.com/ai-gateway/reference/pricing/
- Cloudflare AI Gateway limits: https://developers.cloudflare.com/ai-gateway/reference/limits/
- Kong AI Gateway: https://konghq.com/products/kong-ai-gateway
- Kong pricing: https://konghq.com/pricing
- TrueFoundry AI Gateway: https://www.truefoundry.com/ai-gateway
- TrueFoundry pricing: https://www.truefoundry.com/pricing
- Martian: https://withmartian.com/
- vLLM Semantic Router docs: https://vllm-sr.ai/docs/intro/
- vLLM Semantic Router GitHub: https://github.com/vllm-project/semantic-router
- NVIDIA NeMo Switchyard docs: https://nvidia-nemo.github.io/Switchyard/
- NVIDIA NeMo Switchyard GitHub: https://github.com/NVIDIA-NeMo/Switchyard

## Complement Versus Compete

vLLM Semantic Router is a programmable Mixture-of-Models routing layer. Treat it as a complement, not a replacement, for Metrum AI Router. Kubernetes layer-boundary specs keep the split explicit:

- [docs/specs/kubernetes-deployment.md](specs/kubernetes-deployment.md)
- [docs/specs/kubernetes-deployment-31aug2026.md](specs/kubernetes-deployment-31aug2026.md)

Semantic Router does not enforce caller quotas, choose a model group, own provider credentials, or choose a serving replica. Metrum AI Router owns those deployment controls on the default Client → Metrum → upstream path. Optional Semantic Router placement is advisory and off the mandatory inbound hop.

NVIDIA NeMo Switchyard is an open-source agent model-routing library (and optional gateway/plugin/proxy paths). As of 2026-09-19 its README publishes Pre-1.0 maturity: library components are Alpha or Beta, and the standalone proxy is Demo-only and not for production. Date any public maturity claim to the day it was re-checked; do not freeze a stale pre-release label.

## Claim Safety

Safe public claims:

- Metrum AI Router supports deployment-defined model groups.
- Metrum AI Router supports OpenAI Chat, OpenAI Responses, and Anthropic Messages API shapes.
- Metrum AI Router supports dialect-specific tool routing based on configured metadata.
- Metrum AI Router supports image-aware routing when upstream metadata includes validated image modality.
- Metrum AI Router records request-time cost values in usage rows.
- Metrum AI Router can route to private OpenAI-compatible upstreams such as vLLM and SGLang when configured and validated.
- vLLM Semantic Router complements Metrum AI Router as an optional Mixture-of-Models classification/advisory layer; it is not a substitute for caller-key governance, model groups, credential ownership, or replica selection.
- Switchyard is an open-source agent model-routing library whose published Pre-1.0 maturity (as of 2026-09-19) includes Alpha/Beta library components and a Demo standalone proxy marked not for production.

Avoid or qualify:

- Exact competitor prices.
- Claims that competitors lack a feature unless primary docs clearly show that.
- Claims that Metrum AI Router has enterprise dashboard/SSO/compliance UI features unless implemented.
- Claims that the router automatically improves quality or cost for every workload.
- Undated Switchyard maturity labels, or asserting production readiness beyond what Switchyard publishes.
