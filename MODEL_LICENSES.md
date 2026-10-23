# Model Licenses

## No bundled model weights

This repository does not contain model weights. A search of tracked files found
no common weight artifacts such as `.safetensors`, `.gguf`, `.onnx`, `.pt`,
`.pth`, `.ckpt`, `.tflite`, `.pb`, `.h5`, or model-weight `.bin` files. The
project license therefore does not grant rights to any model weights.

## Provider and model references

`config.example.yaml` contains provider model catalogs and routing examples.
Those entries are identifiers and metadata for separately operated upstream
services; they do not bundle or sublicense the referenced models. Operators
must review and comply with each provider's and model owner's terms before use.
The repository does not contain a locally authoritative, complete set of those
terms.

Capability and evaluation material in `docs/`, `scripts/`, `examples/`,
`evaluators/`, and `tests/` also accepts or shows provider/model identifiers.
These are request, smoke-test, or benchmark references rather than downloads
or grants of model rights.

## Known model download surfaces

The checked-in local-serving Kubernetes examples pass these repository IDs to
vLLM with `--model`, which can cause the serving environment to obtain weights
that are not included here:

The authoritative Hugging Face metadata inspected on 2026-09-03 declared
Apache-2.0 for each repository below and reported the shown then-current
`main` revision. The checked-in manifests use repository IDs without revision
pins, so these hashes are audit observations, not deployment locks.

| Referenced model repository ID | 2026-09-03 observed revision | Checked-in use and disposition |
| --- | --- | --- |
| [`Qwen/Qwen3.5-4B`](https://huggingface.co/Qwen/Qwen3.5-4B) | `851bf6e806efd8d0a36b00ddf55e13ccb7b8cd0a` | AMD local-serving manifest and NVIDIA B200 intent; external, unpinned download |
| [`Qwen/Qwen3-8B`](https://huggingface.co/Qwen/Qwen3-8B) | `b968826d9c46dd6066d109eabc6255188de91218` | AMD/NVIDIA local-serving manifests and intents; external, unpinned download |
| [`Qwen/Qwen3-1.7B`](https://huggingface.co/Qwen/Qwen3-1.7B) | `70d244cc86ccca08cf5af4e1e306ecf908b1ad5e` | AMD/NVIDIA local-serving manifests and intents; external, unpinned download |
| [`Qwen/Qwen3-4B-Instruct-2507`](https://huggingface.co/Qwen/Qwen3-4B-Instruct-2507) | `cdbee75f17c01a7cc42f958dc650907174af0554` | NVIDIA local-serving manifest and intent; external, unpinned download |
| [`Qwen/Qwen3.8-27B`](https://huggingface.co/Qwen/Qwen3.8-27B) | `1d4bf0f2ff6012fd82039f2fa52739d0dd7c60c0` | NVIDIA B200 intent; external, unpinned download |

At the observed revisions these repositories were public and ungated. Their
Apache-2.0 declarations generally permit commercial use and redistribution
subject to the license conditions, but no weights are included in router
artifacts and this inventory is not a legal clearance for later revisions.
Before an operator downloads or redistributes weights, pin the exact revision
and retain its model card, license, any NOTICE file, modification notices, and
third-party component terms. Model names and owner names do not confer
trademark endorsement rights.

`docs/SELF_HOSTED_UPSTREAMS.md` additionally uses model IDs such as
`qwen3-coder-tools` and `qwen-vl` as operator-supplied examples; it does not
pin a model artifact or its terms.

## Dataset and evaluator download surfaces

The LiveCodeBench evaluator contract pins source code revision
`28fef95ea8c9f7a547c8329f2cd3d32b92c1fa24`. The authoritative license file at
that revision is MIT, copyright 2024 LiveCodeBench. The contract separately
requests Hugging Face dataset `livecodebench/code_generation_lite`, release
`release_v6`, through `datasets==3.5.0`. The dataset's observed revision on
2026-09-03 was `0fe84c3912ea0c4d4a78037083943e8f0c4dd505`, but the contract does not pin
that repository revision and its dataset card declares only the ambiguous
license value `cc`. The card says problems are collected from LeetCode,
AtCoder, and Codeforces without identifying a specific Creative Commons
license or resolving the source sites' redistribution terms.

LiveCodeBench code and downloaded dataset content are test/evaluation inputs;
normal router binary, archive, and OCI-image recipes do not ship them. The
dataset license, individual problem provenance, and redistribution permission
remain unresolved. Do not redistribute the dataset or treat the MIT code
license as covering its problem content without authoritative terms.

The two checked-in outcome-routing JSON datasets are repository examples, not
release-package inputs. They appear synthetic, but no adjacent authorship or
source record proves that conclusion. Confirm their origin and that exemplar
text was not copied from a restricted benchmark before redistributing the
source repository.

## Fine-tuning example pins (2026-10-23)

`examples/fine-tune-qwen3p8-27b/` pins external
`Qwen/Qwen3.8-27B@1d4bf0f2ff6012fd82039f2fa52739d0dd7c60c0`.
Authenticated Hub metadata and the pinned LICENSE were rechecked: public,
ungated, Apache-2.0. No weights are bundled.

Training data: `glaiveai/glaive-function-calling-v2` at
`e7f4b6456019f5d8bcb991ef0dd67d8ff23221ac`, Apache-2.0 per pinned dataset
README/metadata, ungated. The converter modifies the upstream representation
and retains conservative first turns only. Keep real data outside Git and retain
Glaive AI attribution and any required notices.

Superseded run-2 preparation / potential licensed collection task seeds (#355):
**current run2 must train on #358 router-captured exports, not this download**.
`OpenAssistant/oasst1` at
`fdf72ae0827c1cda404aff25b6603abec9e3399b`, Apache-2.0 per pinned Hub card and
repository LICENSE, public/ungated. Ready-for-export archive SHA256
`286a6e9a5a413b3272ae9c0b5a20d327983dea1c24342ae28cb244a6da65185c`.
Credit Köpf et al., *OpenAssistant Conversations — Democratizing Large Language
Model Alignment* (2023), arXiv:2304.07327, and LAION/OpenAssistant contributors.
The converter selects reviewed English nonsynthetic rank0 root replies, drops
metadata/known PII or spam flags/control-token literals, and modifies the source
representation. Retain upstream license/notices; neither the corpus nor weights
are bundled. Separate arithmetic format-rehearsal labels are deterministically
Metrum-authored Apache-2.0 examples, not human judgments or benchmark answers.

Judge calibration: `lmsys/mt_bench_human_judgments` at
`f7d2896d2cc5d80f8b55c2bbc722613555233c25`, CC-BY-4.0 per pinned README and
metadata, ungated. Use the public `human` split, not generated judge labels.
Credit Zheng et al., *Judging LLM-as-a-Judge with MT-Bench and Chatbot Arena*
(2023), arXiv:2306.05685. Calibration selects one human preference per distinct
question; only scalar agreement and question IDs enter public evidence, not the
underlying conversations. Dataset licensing is not an endorsement or a grant of
rights to the models whose outputs the dataset contains.

## Remaining unknown terms and operator responsibility

The exact Qwen and LiveCodeBench evidence above is limited to the named
repositories and observed revisions. No complete terms set was fetched for
the many provider/model identifiers used only as remote-service metadata in
configuration, documentation, smokes, or benchmarks. Provider service terms,
acceptable-use policies, geography restrictions, and later model revisions
may differ and remain outside this inventory.

Before downloading, deploying, fine-tuning, redistributing, or invoking a
model, the operator must identify the exact artifact and revision, retrieve its
current authoritative terms from the model owner or provider, confirm that the
intended use and distribution are permitted, and retain the required license
and notice evidence. Model access through a provider does not imply permission
to redistribute that model's weights.
