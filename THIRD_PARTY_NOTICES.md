# Third-Party Notices

Metrum AI Router includes, builds with, or refers to the dependencies below. The Apache-2.0 license in `LICENSE` applies only to first-party content. Metadata was audited on 2026-09-10 from pinned manifests and locks, with a focused version reconciliation against `go.mod` for redistributed Go modules. Package metadata is evidence, not a legal conclusion.

## Inventory summary

| Ecosystem | Unique pinned records | Distribution role |
| --- | ---: | --- |
| Go | 88 | Imported by repository Go builds and compiled into shipped binaries |
| npm | 1462 | Documentation/admin build inputs and browser artifacts; dashboard operator/development tooling |
| Python | 39 | Tests and evaluation tooling; not shipped in the router binary |
| Vendored JavaScript | 2 | Checked-in browser assets shipped with documentation |
| **Total** | **1591** | Deduplicated by ecosystem, name, version, license, and homepage |

## Dependency records

| Ecosystem | Name | Version | License | Homepage | Scope / surface |
| --- | --- | --- | --- | --- | --- |
| Go | github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream | v1.7.24 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2) | shipped binary; Bedrock streaming |
| Go | github.com/aws/aws-sdk-go-v2/internal/v4a | v1.5.7 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2) | shipped binary; AWS signing |
| Go | github.com/aws/aws-sdk-go-v2/service/bedrockruntime | v1.63.7 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2) | shipped binary; Bedrock Converse adapter |
| Go | github.com/aws/aws-sdk-go-v2/config | v1.33.10 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/config) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/credentials | v1.20.10 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/credentials) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/feature/ec2/imds | v1.20.4 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/feature/ec2/imds) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/internal/configsources | v1.5.7 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/internal/configsources) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 | v2.8.7 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/internal/endpoints) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/internal/ini | v1.8.6 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/internal/ini) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/service/eks | v1.75.0 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/service/eks) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding | v1.13.22 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/service/internal/presigned-url | v1.14.7 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/service/internal/presigned-url) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/service/rds | v1.101.0 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/service/rds) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/service/secretsmanager | v1.39.0 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/service/secretsmanager) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/service/signin | v1.10.5 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/service/signin) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/service/ssm | v1.68.0 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/service/ssm) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/service/ssooidc | v1.43.5 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/service/ssooidc) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/service/sso | v1.38.5 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/service/sso) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2/service/sts | v1.51.5 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2/service/sts) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/aws-sdk-go-v2 | v1.47.4 | Apache-2.0 | [project](https://github.com/aws/aws-sdk-go-v2) | shipped binary; packages imported by repository Go builds |
| Go | github.com/aws/smithy-go | v1.28.6 | Apache-2.0 | [project](https://github.com/aws/smithy-go) | shipped binary; packages imported by repository Go builds |
| Go | github.com/bmatcuk/doublestar/v4 | v4.6.1 | MIT | [project](https://github.com/bmatcuk/doublestar) | shipped binary; packages imported by repository Go builds |
| Go | github.com/casbin/casbin/v2 | v2.134.0 | Apache-2.0 | [project](https://github.com/casbin/casbin) | shipped binary; packages imported by repository Go builds |
| Go | github.com/casbin/govaluate | v1.3.0 | MIT | [project](https://github.com/casbin/govaluate) | shipped binary; packages imported by repository Go builds |
| Go | github.com/cespare/xxhash/v2 | v2.3.0 | MIT | [project](https://github.com/cespare/xxhash) | shipped binary; packages imported by repository Go builds |
| Go | github.com/coreos/go-oidc/v3 | v3.19.0 | Apache-2.0 | [project](https://github.com/coreos/go-oidc) | shipped binary; packages imported by repository Go builds |
| Go | github.com/davecgh/go-spew | v1.1.1 | ISC | [project](https://github.com/davecgh/go-spew) | shipped binary; packages imported by repository Go builds |
| Go | github.com/dlclark/regexp2/v2 | v2.2.1 | MIT | [project](https://github.com/dlclark/regexp2) | shipped binary; packages imported by repository Go builds |
| Go | github.com/dop251/goja | v0.0.0-20260607120635-348e6bea910d | MIT | [project](https://github.com/dop251/goja) | shipped binary; packages imported by repository Go builds |
| Go | github.com/dustin/go-humanize | v1.0.1 | MIT | [project](https://github.com/dustin/go-humanize) | shipped binary; packages imported by repository Go builds |
| Go | github.com/emicklei/go-restful/v3 | v3.12.2 | MIT | [project](https://github.com/emicklei/go-restful) | shipped binary; packages imported by repository Go builds |
| Go | github.com/evanw/esbuild | v0.28.1 | MIT | [project](https://github.com/evanw/esbuild) | shipped binary; packages imported by repository Go builds |
| Go | github.com/fxamacker/cbor/v2 | v2.9.0 | MIT | [project](https://github.com/fxamacker/cbor) | shipped binary; packages imported by repository Go builds |
| Go | github.com/glebarez/go-sqlite | v1.21.2 | BSD-3-Clause | [project](https://github.com/glebarez/go-sqlite) | shipped binary; packages imported by repository Go builds |
| Go | github.com/glebarez/sqlite | v1.11.0 | MIT | [project](https://github.com/glebarez/sqlite) | shipped binary; packages imported by repository Go builds |
| Go | github.com/go-jose/go-jose/v4 | v4.1.4 | Apache-2.0 | [project](https://github.com/go-jose/go-jose) | shipped binary; packages imported by repository Go builds |
| Go | github.com/go-logr/logr | v1.4.3 | Apache-2.0 | [project](https://github.com/go-logr/logr) | shipped binary; packages imported by repository Go builds |
| Go | github.com/go-openapi/jsonpointer | v0.21.0 | Apache-2.0 | [project](https://github.com/go-openapi/jsonpointer) | shipped binary; packages imported by repository Go builds |
| Go | github.com/go-openapi/jsonreference | v0.20.2 | Apache-2.0 | [project](https://github.com/go-openapi/jsonreference) | shipped binary; packages imported by repository Go builds |
| Go | github.com/go-openapi/swag | v0.23.0 | Apache-2.0 | [project](https://github.com/go-openapi/swag) | shipped binary; packages imported by repository Go builds |
| Go | github.com/go-sourcemap/sourcemap | v2.1.3+incompatible | BSD-2-Clause | [project](https://github.com/go-sourcemap/sourcemap) | shipped binary; packages imported by repository Go builds |
| Go | github.com/google/gnostic-models | v0.7.0 | Apache-2.0 | [project](https://github.com/google/gnostic-models) | shipped binary; packages imported by repository Go builds |
| Go | github.com/google/pprof | v0.0.0-20250403155104-27863c87afa6 | Apache-2.0 | [project](https://github.com/google/pprof) | shipped binary; packages imported by repository Go builds |
| Go | github.com/google/uuid | v1.6.0 | BSD-3-Clause | [project](https://github.com/google/uuid) | shipped binary; packages imported by repository Go builds |
| Go | github.com/jackc/pgpassfile | v1.0.0 | MIT | [project](https://github.com/jackc/pgpassfile) | shipped binary; packages imported by repository Go builds |
| Go | github.com/jackc/pgservicefile | v0.0.0-20240606120523-5a60cdf6a761 | MIT | [project](https://github.com/jackc/pgservicefile) | shipped binary; packages imported by repository Go builds |
| Go | github.com/jackc/pgx/v5 | v5.9.0 | MIT | [project](https://github.com/jackc/pgx) | shipped binary; packages imported by repository Go builds |
| Go | github.com/jackc/puddle/v2 | v2.2.2 | MIT | [project](https://github.com/jackc/puddle) | shipped binary; packages imported by repository Go builds |
| Go | github.com/jinzhu/inflection | v1.0.0 | MIT | [project](https://github.com/jinzhu/inflection) | shipped binary; packages imported by repository Go builds |
| Go | github.com/jinzhu/now | v1.1.5 | MIT | [project](https://github.com/jinzhu/now) | shipped binary; packages imported by repository Go builds |
| Go | github.com/josharian/intern | v1.0.0 | MIT | [project](https://github.com/josharian/intern) | shipped binary; packages imported by repository Go builds |
| Go | github.com/json-iterator/go | v1.1.12 | MIT | [project](https://github.com/json-iterator/go) | shipped binary; packages imported by repository Go builds |
| Go | github.com/mailru/easyjson | v0.7.7 | MIT | [project](https://github.com/mailru/easyjson) | shipped binary; packages imported by repository Go builds |
| Go | github.com/modern-go/concurrent | v0.0.0-20180306012644-bacd9c7ef1dd | Apache-2.0 | [project](https://github.com/modern-go/concurrent) | shipped binary; packages imported by repository Go builds |
| Go | github.com/modern-go/reflect2 | v1.0.3-0.20250322232337-35a7c28c31ee | Apache-2.0 | [project](https://github.com/modern-go/reflect2) | shipped binary; packages imported by repository Go builds |
| Go | github.com/munnerz/goautoneg | v0.0.0-20191010083416-a7dc8b61c822 | BSD-3-Clause | [project](https://github.com/munnerz/goautoneg) | shipped binary; packages imported by repository Go builds |
| Go | github.com/redis/go-redis/v9 | v9.21.0 | BSD-2-Clause | [project](https://github.com/redis/go-redis) | shipped binary; packages imported by repository Go builds |
| Go | github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause | [project](https://github.com/remyoudompheng/bigfft) | shipped binary; packages imported by repository Go builds |
| Go | github.com/x448/float16 | v0.8.4 | MIT | [project](https://github.com/x448/float16) | shipped binary; packages imported by repository Go builds |
| Go | go.uber.org/atomic | v1.11.0 | MIT | [project](https://pkg.go.dev/go.uber.org/atomic) | shipped binary; packages imported by repository Go builds |
| Go | go.yaml.in/yaml/v2 | v2.4.3 | Apache-2.0 | [project](https://pkg.go.dev/go.yaml.in/yaml/v2) | shipped binary; packages imported by repository Go builds |
| Go | go.yaml.in/yaml/v3 | v3.0.4 | Apache-2.0 | [project](https://pkg.go.dev/go.yaml.in/yaml/v3) | shipped binary; packages imported by repository Go builds |
| Go | golang.org/x/crypto | v0.56.0 | BSD-3-Clause | [project](https://pkg.go.dev/golang.org/x/crypto) | shipped binary; packages imported by repository Go builds |
| Go | golang.org/x/net | v0.57.0 | BSD-3-Clause | [project](https://pkg.go.dev/golang.org/x/net) | shipped binary; packages imported by repository Go builds |
| Go | golang.org/x/oauth2 | v0.36.0 | BSD-3-Clause | [project](https://pkg.go.dev/golang.org/x/oauth2) | shipped binary; packages imported by repository Go builds |
| Go | golang.org/x/sync | v0.20.0 | BSD-3-Clause | [project](https://pkg.go.dev/golang.org/x/sync) | shipped binary; packages imported by repository Go builds |
| Go | golang.org/x/sys | v0.42.0 | BSD-3-Clause | [project](https://pkg.go.dev/golang.org/x/sys) | shipped binary; packages imported by repository Go builds |
| Go | golang.org/x/term | v0.37.0 | BSD-3-Clause | [project](https://pkg.go.dev/golang.org/x/term) | shipped binary; packages imported by repository Go builds |
| Go | golang.org/x/text | v0.31.0 | BSD-3-Clause | [project](https://pkg.go.dev/golang.org/x/text) | shipped binary; packages imported by repository Go builds |
| Go | golang.org/x/time | v0.9.0 | BSD-3-Clause | [project](https://pkg.go.dev/golang.org/x/time) | shipped binary; packages imported by repository Go builds |
| Go | google.golang.org/protobuf | v1.36.8 | BSD-3-Clause | [project](https://pkg.go.dev/google.golang.org/protobuf) | shipped binary; packages imported by repository Go builds |
| Go | gopkg.in/evanphx/json-patch.v4 | v4.13.0 | BSD-3-Clause | [project](https://pkg.go.dev/gopkg.in/evanphx/json-patch.v4) | shipped binary; packages imported by repository Go builds |
| Go | gopkg.in/inf.v0 | v0.9.1 | BSD-3-Clause | [project](https://pkg.go.dev/gopkg.in/inf.v0) | shipped binary; packages imported by repository Go builds |
| Go | gopkg.in/yaml.v3 | v3.0.1 | Apache-2.0 | [project](https://pkg.go.dev/gopkg.in/yaml.v3) | shipped binary; packages imported by repository Go builds |
| Go | gorm.io/driver/postgres | v1.6.0 | MIT | [project](https://pkg.go.dev/gorm.io/driver/postgres) | shipped binary; packages imported by repository Go builds |
| Go | gorm.io/gorm | v1.31.1 | MIT | [project](https://pkg.go.dev/gorm.io/gorm) | shipped binary; packages imported by repository Go builds |
| Go | k8s.io/apimachinery | v0.35.0 | Apache-2.0 | [project](https://pkg.go.dev/k8s.io/apimachinery) | shipped binary; packages imported by repository Go builds |
| Go | k8s.io/api | v0.35.0 | Apache-2.0 | [project](https://pkg.go.dev/k8s.io/api) | shipped binary; packages imported by repository Go builds |
| Go | k8s.io/client-go | v0.35.0 | Apache-2.0 | [project](https://pkg.go.dev/k8s.io/client-go) | shipped binary; packages imported by repository Go builds |
| Go | k8s.io/klog/v2 | v2.130.1 | Apache-2.0 | [project](https://pkg.go.dev/k8s.io/klog/v2) | shipped binary; packages imported by repository Go builds |
| Go | k8s.io/kube-openapi | v0.0.0-20250910181357-589584f1c912 | Apache-2.0 | [project](https://pkg.go.dev/k8s.io/kube-openapi) | shipped binary; packages imported by repository Go builds |
| Go | k8s.io/utils | v0.0.0-20251002143259-bc988d571ff4 | Apache-2.0 | [project](https://pkg.go.dev/k8s.io/utils) | shipped binary; packages imported by repository Go builds |
| Go | modernc.org/libc | v1.72.3 | BSD-3-Clause | [project](https://pkg.go.dev/modernc.org/libc) | shipped binary; packages imported by repository Go builds |
| Go | modernc.org/mathutil | v1.7.1 | BSD-3-Clause | [project](https://pkg.go.dev/modernc.org/mathutil) | shipped binary; transitive dependency of `modernc.org/libc`, `modernc.org/memory`, and `modernc.org/sqlite`; required binary-redistribution attribution is in root `NOTICE` |
| Go | modernc.org/memory | v1.11.0 | BSD-3-Clause | [project](https://pkg.go.dev/modernc.org/memory) | shipped binary; packages imported by repository Go builds |
| Go | modernc.org/sqlite | v1.52.0 | BSD-3-Clause | [project](https://pkg.go.dev/modernc.org/sqlite) | shipped binary; packages imported by repository Go builds |
| Go | sigs.k8s.io/json | v0.0.0-20250730193827-2d320260d730 | Apache-2.0 | [project](https://pkg.go.dev/sigs.k8s.io/json) | shipped binary; packages imported by repository Go builds |
| Go | sigs.k8s.io/randfill | v1.0.0 | Apache-2.0 | [project](https://pkg.go.dev/sigs.k8s.io/randfill) | shipped binary; packages imported by repository Go builds |
| Go | sigs.k8s.io/structured-merge-diff/v6 | v6.3.0 | Apache-2.0 | [project](https://pkg.go.dev/sigs.k8s.io/structured-merge-diff/v6) | shipped binary; packages imported by repository Go builds |
| Go | sigs.k8s.io/yaml | v1.6.0 | Apache-2.0 | [project](https://pkg.go.dev/sigs.k8s.io/yaml) | shipped binary; packages imported by repository Go builds |
| npm | @algolia/abtesting | 1.20.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript/tree/main/packages/abtesting#readme) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/autocomplete-core | 1.19.2 | MIT | [project](https://github.com/algolia/autocomplete) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/autocomplete-core | 1.19.8 | MIT | [project](https://github.com/algolia/autocomplete) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/autocomplete-plugin-algolia-insights | 1.19.2 | MIT | [project](https://github.com/algolia/autocomplete) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/autocomplete-plugin-algolia-insights | 1.19.8 | MIT | [project](https://github.com/algolia/autocomplete) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/autocomplete-shared | 1.19.2 | MIT | [project](https://github.com/algolia/autocomplete) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/autocomplete-shared | 1.19.8 | MIT | [project](https://github.com/algolia/autocomplete) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/client-abtesting | 5.54.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript/tree/main/packages/client-abtesting#readme) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/client-analytics | 5.54.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript/tree/main/packages/client-analytics#readme) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/client-common | 5.54.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript#readme) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/client-insights | 5.54.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript/tree/main/packages/client-insights#readme) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/client-personalization | 5.54.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript/tree/main/packages/client-personalization#readme) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/client-query-suggestions | 5.54.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript/tree/main/packages/client-query-suggestions#readme) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/client-search | 5.54.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript/tree/main/packages/client-search#readme) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/events | 4.0.1 | MIT | [project](https://github.com/algolia/events) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/ingestion | 1.54.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript/tree/main/packages/ingestion#readme) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/monitoring | 1.54.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript/tree/main/packages/monitoring#readme) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/recommend | 5.54.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript/tree/main/packages/recommend#readme) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/requester-browser-xhr | 5.54.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript#readme) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/requester-fetch | 5.54.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript#readme) | build input / potential shipped browser code; documentation web app |
| npm | @algolia/requester-node-http | 5.54.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript#readme) | build input / potential shipped browser code; documentation web app |
| npm | @alloc/quick-lru | 5.2.0 | MIT | [project](sindresorhus/quick-lru) | shipped binary/web artifact; embedded admin app |
| npm | @antfu/install-pkg | 1.1.0 | MIT | [project](https://github.com/antfu/install-pkg#readme) | build input / potential shipped browser code; documentation web app |
| npm | @babel/code-frame | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-code-frame) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/compat-data | 7.29.7 | MIT | [project](https://github.com/babel/babel) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/core | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-core) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/generator | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-generator) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/helper-annotate-as-pure | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-helper-annotate-as-pure) | build input / potential shipped browser code; documentation web app |
| npm | @babel/helper-compilation-targets | 7.29.7 | MIT | [project](https://github.com/babel/babel) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/helper-create-class-features-plugin | 7.29.7 | MIT | [project](https://github.com/babel/babel) | build input / potential shipped browser code; documentation web app |
| npm | @babel/helper-create-regexp-features-plugin | 7.29.7 | MIT | [project](https://github.com/babel/babel) | build input / potential shipped browser code; documentation web app |
| npm | @babel/helper-define-polyfill-provider | 0.6.8 | MIT | [project](https://github.com/babel/babel-polyfills) | build input / potential shipped browser code; documentation web app |
| npm | @babel/helper-globals | 7.29.7 | MIT | [project](https://github.com/babel/babel) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/helper-member-expression-to-functions | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-helper-member-expression-to-functions) | build input / potential shipped browser code; documentation web app |
| npm | @babel/helper-module-imports | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-helper-module-imports) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/helper-module-transforms | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-helper-module-transforms) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/helper-optimise-call-expression | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-helper-optimise-call-expression) | build input / potential shipped browser code; documentation web app |
| npm | @babel/helper-plugin-utils | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-helper-plugin-utils) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/helper-remap-async-to-generator | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-helper-remap-async-to-generator) | build input / potential shipped browser code; documentation web app |
| npm | @babel/helper-replace-supers | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-helper-replace-supers) | build input / potential shipped browser code; documentation web app |
| npm | @babel/helper-skip-transparent-expression-wrappers | 7.29.7 | MIT | [project](https://github.com/babel/babel) | build input / potential shipped browser code; documentation web app |
| npm | @babel/helper-string-parser | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-helper-string-parser) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/helper-validator-identifier | 7.29.7 | MIT | [project](https://github.com/babel/babel) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/helper-validator-option | 7.29.7 | MIT | [project](https://github.com/babel/babel) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/helper-wrap-function | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-helper-wrap-function) | build input / potential shipped browser code; documentation web app |
| npm | @babel/helpers | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-helpers) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/parser | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-parser) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/plugin-bugfix-firefox-class-in-computed-class-key | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-bugfix-firefox-class-in-computed-class-key) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-bugfix-safari-class-field-initializer-scope | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-bugfix-safari-class-field-initializer-scope) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-bugfix-safari-id-destructuring-collision-in-function-expression | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-bugfix-safari-id-destructuring-collision-in-function-expression) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-bugfix-safari-rest-destructuring-rhs-array | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-bugfix-safari-rest-destructuring-rhs-array) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-bugfix-v8-spread-parameters-in-optional-chaining | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-bugfix-v8-spread-parameters-in-optional-chaining) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-bugfix-v8-static-class-fields-redefine-readonly | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-bugfix-v8-static-class-fields-redefine-readonly) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-proposal-private-property-in-object | 7.21.0-placeholder-for-preset-env.2 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-proposal-private-property-in-object) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-syntax-dynamic-import | 7.8.3 | MIT | [project](https://github.com/babel/babel/tree/master/packages/babel-plugin-syntax-dynamic-import) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-syntax-import-assertions | 7.29.7 | MIT | [project](https://github.com/babel/babel) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-syntax-import-attributes | 7.29.7 | MIT | [project](https://github.com/babel/babel) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-syntax-jsx | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-syntax-jsx) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-syntax-typescript | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-syntax-typescript) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-syntax-unicode-sets-regex | 7.18.6 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-syntax-unicode-sets-regex) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-arrow-functions | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-arrow-functions) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-async-generator-functions | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-async-generator-functions) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-async-to-generator | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-async-to-generator) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-block-scoped-functions | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-block-scoped-functions) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-block-scoping | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-block-scoping) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-class-properties | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-class-properties) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-class-static-block | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-class-static-block) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-classes | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-classes) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-computed-properties | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-computed-properties) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-destructuring | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-destructuring) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-dotall-regex | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-dotall-regex) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-duplicate-keys | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-duplicate-keys) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-duplicate-named-capturing-groups-regex | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-duplicate-named-capturing-groups-regex) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-dynamic-import | 7.29.7 | MIT | [project](https://github.com/babel/babel) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-explicit-resource-management | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-explicit-resource-management) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-exponentiation-operator | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-exponentiation-operator) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-export-namespace-from | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-export-namespace-from) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-for-of | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-for-of) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-function-name | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-function-name) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-json-strings | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-json-strings) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-literals | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-literals) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-logical-assignment-operators | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-logical-assignment-operators) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-member-expression-literals | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-member-expression-literals) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-modules-amd | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-modules-amd) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-modules-commonjs | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-modules-commonjs) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-modules-systemjs | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-modules-systemjs) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-modules-umd | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-modules-umd) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-named-capturing-groups-regex | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-named-capturing-groups-regex) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-new-target | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-new-target) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-nullish-coalescing-operator | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-nullish-coalescing-operator) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-numeric-separator | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-numeric-separator) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-object-rest-spread | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-object-rest-spread) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-object-super | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-object-super) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-optional-catch-binding | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-optional-catch-binding) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-optional-chaining | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-optional-chaining) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-parameters | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-parameters) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-private-methods | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-private-methods) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-private-property-in-object | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-private-property-in-object) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-property-literals | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-property-literals) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-react-constant-elements | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-react-constant-elements) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-react-display-name | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-react-display-name) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-react-jsx-development | 7.29.7 | MIT | [project](https://github.com/babel/babel) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-react-jsx-self | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-react-jsx-self) | shipped binary/web artifact; embedded admin app |
| npm | @babel/plugin-transform-react-jsx-source | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-react-jsx-source) | shipped binary/web artifact; embedded admin app |
| npm | @babel/plugin-transform-react-jsx | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-react-jsx) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-react-pure-annotations | 7.29.7 | MIT | [project](https://github.com/babel/babel) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-regenerator | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-regenerator) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-regexp-modifiers | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-regexp-modifiers) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-reserved-words | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-reserved-words) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-runtime | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-runtime) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-shorthand-properties | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-shorthand-properties) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-spread | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-spread) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-sticky-regex | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-sticky-regex) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-template-literals | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-template-literals) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-typeof-symbol | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-typeof-symbol) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-typescript | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-typescript) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-unicode-escapes | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-unicode-escapes) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-unicode-property-regex | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-unicode-property-regex) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-unicode-regex | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-unicode-regex) | build input / potential shipped browser code; documentation web app |
| npm | @babel/plugin-transform-unicode-sets-regex | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-plugin-transform-unicode-sets-regex) | build input / potential shipped browser code; documentation web app |
| npm | @babel/preset-env | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-preset-env) | build input / potential shipped browser code; documentation web app |
| npm | @babel/preset-modules | 0.1.6-no-external-plugins | MIT | [project](https://github.com/babel/preset-modules) | build input / potential shipped browser code; documentation web app |
| npm | @babel/preset-react | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-preset-react) | build input / potential shipped browser code; documentation web app |
| npm | @babel/preset-typescript | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-preset-typescript) | build input / potential shipped browser code; documentation web app |
| npm | @babel/runtime | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-runtime) | build input / potential shipped browser code; documentation web app |
| npm | @babel/template | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-template) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/traverse | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-traverse) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @babel/types | 7.29.7 | MIT | [project](https://babel.dev/docs/en/next/babel-types) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @braintree/sanitize-url | 7.1.2 | MIT | [project](https://github.com/braintree/sanitize-url#readme) | build input / potential shipped browser code; documentation web app |
| npm | @chevrotain/types | 11.1.2 | Apache-2.0 | [project](https://chevrotain.io/documentation/) | build input / potential shipped browser code; documentation web app |
| npm | @colors/colors | 1.5.0 | MIT | [project](https://github.com/DABH/colors.js) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/cascade-layer-name-parser | 2.0.5 | MIT | [project](https://github.com/csstools/postcss-plugins/tree/main/packages/cascade-layer-name-parser#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/color-helpers | 5.1.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/packages/color-helpers#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/css-calc | 2.1.4 | MIT | [project](https://github.com/csstools/postcss-plugins/tree/main/packages/css-calc#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/css-color-parser | 3.1.0 | MIT | [project](https://github.com/csstools/postcss-plugins/tree/main/packages/css-color-parser#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/css-parser-algorithms | 3.0.5 | MIT | [project](https://github.com/csstools/postcss-plugins/tree/main/packages/css-parser-algorithms#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/css-tokenizer | 3.0.4 | MIT | [project](https://github.com/csstools/postcss-plugins/tree/main/packages/css-tokenizer#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/media-query-list-parser | 4.0.3 | MIT | [project](https://github.com/csstools/postcss-plugins/tree/main/packages/media-query-list-parser#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-alpha-function | 1.0.1 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-alpha-function#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-cascade-layers | 5.0.2 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-cascade-layers#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-color-function-display-p3-linear | 1.0.1 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-color-function-display-p3-linear#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-color-function | 4.0.12 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-color-function#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-color-mix-function | 3.0.12 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-color-mix-function#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-color-mix-variadic-function-arguments | 1.0.2 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-color-mix-variadic-function-arguments#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-content-alt-text | 2.0.8 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-content-alt-text#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-contrast-color-function | 2.0.12 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-contrast-color-function#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-exponential-functions | 2.0.9 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-exponential-functions#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-font-format-keywords | 4.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-font-format-keywords#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-gamut-mapping | 2.0.11 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-gamut-mapping#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-gradients-interpolation-method | 5.0.12 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-gradients-interpolation-method#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-hwb-function | 4.0.12 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-hwb-function#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-ic-unit | 4.0.4 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-ic-unit#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-initial | 2.0.1 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-initial#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-is-pseudo-class | 5.0.3 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-is-pseudo-class#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-light-dark-function | 2.0.11 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-light-dark-function#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-logical-float-and-clear | 3.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-logical-float-and-clear#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-logical-overflow | 2.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-logical-overflow#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-logical-overscroll-behavior | 2.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-logical-overscroll-behavior#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-logical-resize | 3.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-logical-resize#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-logical-viewport-units | 3.0.4 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-logical-viewport-units#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-media-minmax | 2.0.9 | MIT | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-media-minmax#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-media-queries-aspect-ratio-number-values | 3.0.5 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-media-queries-aspect-ratio-number-values#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-nested-calc | 4.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-nested-calc#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-normalize-display-values | 4.0.1 | MIT-0 | [project](https://github.com/csstools/postcss-plugins) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-oklab-function | 4.0.12 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-oklab-function#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-position-area-property | 1.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-position-area-property#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-progressive-custom-properties | 4.2.1 | MIT-0 | [project](https://github.com/csstools/postcss-plugins) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-property-rule-prelude-list | 1.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-property-rule-prelude-list#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-random-function | 2.0.1 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-random-function#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-relative-color-syntax | 3.0.12 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-relative-color-syntax#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-scope-pseudo-class | 4.0.1 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-scope-pseudo-class#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-sign-functions | 1.1.4 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-sign-functions#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-stepped-value-functions | 4.0.9 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-stepped-value-functions#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-syntax-descriptor-syntax-production | 1.0.1 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-syntax-descriptor-syntax-production#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-system-ui-font-family | 1.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-system-ui-font-family#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-text-decoration-shorthand | 4.0.3 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-text-decoration-shorthand#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-trigonometric-functions | 4.0.9 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-trigonometric-functions#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/postcss-unset-value | 4.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-unset-value#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/selector-resolve-nested | 3.1.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/packages/selector-resolve-nested#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/selector-specificity | 5.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/packages/selector-specificity#readme) | build input / potential shipped browser code; documentation web app |
| npm | @csstools/utilities | 2.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/packages/utilities#readme) | build input / potential shipped browser code; documentation web app |
| npm | @discoveryjs/json-ext | 0.5.7 | MIT | [project](discoveryjs/json-ext) | build input / potential shipped browser code; documentation web app |
| npm | @docsearch/core | 4.6.3 | MIT | [project](https://docsearch.algolia.com) | build input / potential shipped browser code; documentation web app |
| npm | @docsearch/css | 4.6.3 | MIT | [project](https://docsearch.algolia.com) | build input / potential shipped browser code; documentation web app |
| npm | @docsearch/react | 4.6.3 | MIT | [project](https://docsearch.algolia.com) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/babel | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/bundler | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/core | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/cssnano-preset | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/logger | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/mdx-loader | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/module-type-aliases | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/plugin-content-blog | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/plugin-content-docs | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/plugin-content-pages | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/plugin-css-cascade-layers | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/plugin-debug | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/plugin-google-analytics | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/plugin-google-gtag | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/plugin-google-tag-manager | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/plugin-sitemap | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/plugin-svgr | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/preset-classic | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/react-loadable | 6.0.0 | MIT | [project](thejameskyle/react-loadable) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/theme-classic | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/theme-common | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/theme-mermaid | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/theme-search-algolia | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/theme-translations | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/types | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/utils-common | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/utils-validation | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @docusaurus/utils | 3.10.1 | MIT | [project](https://github.com/facebook/docusaurus) | build input / potential shipped browser code; documentation web app |
| npm | @duckdb/duckdb-wasm | 1.33.1-dev57.0 | MIT | [project](https://github.com/duckdb/duckdb-wasm) | development/operator tool; work dashboard |
| npm | @esbuild/aix-ppc64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/android-arm | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/android-arm64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/android-x64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/darwin-arm64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/darwin-x64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/freebsd-arm64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/freebsd-x64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/linux-arm | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/linux-arm64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/linux-ia32 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/linux-loong64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/linux-mips64el | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/linux-ppc64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/linux-riscv64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/linux-s390x | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/linux-x64 | 0.28.1 | MIT | [project](https://github.com/evanw/esbuild) | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/netbsd-arm64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/netbsd-x64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/openbsd-arm64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/openbsd-x64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/openharmony-arm64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/sunos-x64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/win32-arm64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/win32-ia32 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @esbuild/win32-x64 | 0.28.1 | MIT | **Unresolved** | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | @hapi/hoek | 9.3.0 | BSD-3-Clause | [project](https://github.com/hapijs/hoek) | build input / potential shipped browser code; documentation web app |
| npm | @hapi/topo | 5.1.0 | BSD-3-Clause | [project](https://github.com/hapijs/topo) | build input / potential shipped browser code; documentation web app |
| npm | @iconify/types | 2.0.0 | MIT | [project](https://github.com/iconify/iconify) | build input / potential shipped browser code; documentation web app |
| npm | @iconify/utils | 3.1.3 | MIT | [project](https://iconify.design/docs/libraries/utils/) | build input / potential shipped browser code; documentation web app |
| npm | @jest/schemas | 29.6.3 | MIT | [project](https://github.com/jestjs/jest) | build input / potential shipped browser code; documentation web app |
| npm | @jest/types | 29.6.3 | MIT | [project](https://github.com/jestjs/jest) | build input / potential shipped browser code; documentation web app |
| npm | @jridgewell/gen-mapping | 0.3.13 | MIT | [project](https://github.com/jridgewell/sourcemaps/tree/main/packages/gen-mapping) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @jridgewell/remapping | 2.3.5 | MIT | [project](https://github.com/jridgewell/sourcemaps/tree/main/packages/remapping) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @jridgewell/resolve-uri | 3.1.2 | MIT | [project](https://github.com/jridgewell/resolve-uri) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @jridgewell/source-map | 0.3.11 | MIT | [project](https://github.com/jridgewell/sourcemaps/tree/main/packages/source-map) | build input / potential shipped browser code; documentation web app |
| npm | @jridgewell/sourcemap-codec | 1.5.5 | MIT | [project](https://github.com/jridgewell/sourcemaps/tree/main/packages/sourcemap-codec) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @jridgewell/trace-mapping | 0.3.31 | MIT | [project](https://github.com/jridgewell/sourcemaps/tree/main/packages/trace-mapping) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @jsonjoy.com/base64 | 1.1.2 | Apache-2.0 | [project](https://github.com/jsonjoy-com/base64) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/base64 | 17.67.0 | Apache-2.0 | [project](https://github.com/jsonjoy-com/base64) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/buffers | 1.2.1 | Apache-2.0 | [project](https://github.com/jsonjoy-com/buffers) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/buffers | 17.67.0 | Apache-2.0 | [project](https://github.com/jsonjoy-com/buffers) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/codegen | 1.0.0 | Apache-2.0 | [project](https://github.com/jsonjoy-com/codegen) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/codegen | 17.67.0 | Apache-2.0 | [project](https://github.com/jsonjoy-com/codegen) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/fs-core | 4.57.7 | Apache-2.0 | [project](https://github.com/streamich/memfs/tree/master/packages/fs-core) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/fs-fsa | 4.57.7 | Apache-2.0 | [project](https://github.com/streamich/memfs/tree/master/packages/fs-fsa) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/fs-node-builtins | 4.57.7 | Apache-2.0 | [project](https://github.com/streamich/memfs/tree/master/packages/fs-node-builtins) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/fs-node-to-fsa | 4.57.7 | Apache-2.0 | [project](https://github.com/streamich/memfs/tree/master/packages/fs-node-to-fsa) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/fs-node-utils | 4.57.7 | Apache-2.0 | [project](https://github.com/streamich/memfs/tree/master/packages/fs-node-utils) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/fs-node | 4.57.7 | Apache-2.0 | [project](https://github.com/streamich/memfs/tree/master/packages/fs-node) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/fs-print | 4.57.7 | Apache-2.0 | [project](https://github.com/streamich/memfs/tree/master/packages/fs-print) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/fs-snapshot | 4.57.7 | Apache-2.0 | [project](https://github.com/streamich/memfs/tree/master/packages/fs-snapshot) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/json-pack | 1.21.0 | Apache-2.0 | [project](https://github.com/jsonjoy-com/json-pack) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/json-pack | 17.67.0 | Apache-2.0 | [project](https://github.com/jsonjoy-com/json-pack) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/json-pointer | 1.0.2 | Apache-2.0 | [project](https://github.com/jsonjoy-com/json-pointer) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/json-pointer | 17.67.0 | Apache-2.0 | [project](https://github.com/jsonjoy-com/json-pointer) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/util | 1.9.0 | Apache-2.0 | [project](https://github.com/jsonjoy-com/util) | build input / potential shipped browser code; documentation web app |
| npm | @jsonjoy.com/util | 17.67.0 | Apache-2.0 | [project](https://github.com/jsonjoy-com/util) | build input / potential shipped browser code; documentation web app |
| npm | @kurkle/color | 0.3.4 | MIT | [project](https://github.com/kurkle/color#readme) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @leichtgewicht/ip-codec | 2.0.5 | MIT | [project](https://github.com/martinheidegger/ip-codec#readme) | build input / potential shipped browser code; documentation web app |
| npm | @mdx-js/mdx | 3.1.1 | MIT | [project](https://mdxjs.com) | build input / potential shipped browser code; documentation web app |
| npm | @mdx-js/react | 3.1.1 | MIT | [project](https://mdxjs.com) | build input / potential shipped browser code; documentation web app |
| npm | @mermaid-js/layout-elk | 0.1.9 | MIT | [project](https://github.com/mermaid-js/mermaid) | build input / potential shipped browser code; documentation web app |
| npm | @mermaid-js/parser | 1.1.1 | MIT | [project](https://github.com/mermaid-js/mermaid/tree/develop/packages/mermaid/parser/#readme) | build input / potential shipped browser code; documentation web app |
| npm | @napi-rs/lzma-linux-x64-gnu | 1.5.1 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @noble/hashes | 1.4.0 | MIT | [project](https://paulmillr.com/noble/) | build input / potential shipped browser code; documentation web app |
| npm | @nodelib/fs.scandir | 2.1.5 | MIT | [project](https://github.com/nodelib/nodelib/tree/master/packages/fs/fs.scandir) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @nodelib/fs.stat | 2.0.5 | MIT | [project](https://github.com/nodelib/nodelib/tree/master/packages/fs/fs.stat) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @nodelib/fs.walk | 1.2.8 | MIT | [project](https://github.com/nodelib/nodelib/tree/master/packages/fs/fs.walk) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | @observablehq/plot | 0.6.17 | ISC | [project](https://github.com/observablehq/plot) | development/operator tool; work dashboard |
| npm | @peculiar/asn1-cms | 2.8.0 | MIT | [project](https://github.com/PeculiarVentures/asn1-schema/tree/master/packages/cms#readme) | build input / potential shipped browser code; documentation web app |
| npm | @peculiar/asn1-csr | 2.8.0 | MIT | [project](https://github.com/PeculiarVentures/asn1-schema/tree/master/packages/csr#readme) | build input / potential shipped browser code; documentation web app |
| npm | @peculiar/asn1-ecc | 2.8.0 | MIT | [project](https://github.com/PeculiarVentures/asn1-schema/tree/master/packages/ecc#readme) | build input / potential shipped browser code; documentation web app |
| npm | @peculiar/asn1-pfx | 2.8.0 | MIT | [project](https://github.com/PeculiarVentures/asn1-schema/tree/master/packages/pfx#readme) | build input / potential shipped browser code; documentation web app |
| npm | @peculiar/asn1-pkcs8 | 2.8.0 | MIT | [project](https://github.com/PeculiarVentures/asn1-schema/tree/master/packages/pkcs8#readme) | build input / potential shipped browser code; documentation web app |
| npm | @peculiar/asn1-pkcs9 | 2.8.0 | MIT | [project](https://github.com/PeculiarVentures/asn1-schema/tree/master/packages/pkcs9#readme) | build input / potential shipped browser code; documentation web app |
| npm | @peculiar/asn1-rsa | 2.8.0 | MIT | [project](https://github.com/PeculiarVentures/asn1-schema/tree/master/packages/rsa#readme) | build input / potential shipped browser code; documentation web app |
| npm | @peculiar/asn1-schema | 2.8.0 | MIT | [project](https://github.com/PeculiarVentures/asn1-schema/tree/master/packages/schema#readme) | build input / potential shipped browser code; documentation web app |
| npm | @peculiar/asn1-x509-attr | 2.8.0 | MIT | [project](https://github.com/PeculiarVentures/asn1-schema/tree/master/packages/x509-attr#readme) | build input / potential shipped browser code; documentation web app |
| npm | @peculiar/asn1-x509 | 2.8.0 | MIT | [project](https://github.com/PeculiarVentures/asn1-schema/tree/master/packages/x509#readme) | build input / potential shipped browser code; documentation web app |
| npm | @peculiar/utils | 2.0.3 | MIT | [project](https://github.com/PeculiarVentures/pvtsutils#readme) | build input / potential shipped browser code; documentation web app |
| npm | @peculiar/x509 | 1.14.3 | MIT | [project](https://github.com/PeculiarVentures/x509#readme) | build input / potential shipped browser code; documentation web app |
| npm | @playwright/test | 1.61.1 | Apache-2.0 | [project](https://playwright.dev) | build/dev only; embedded admin app |
| npm | @pnpm/config.env-replace | 1.1.0 | MIT | [project](https://bit.cloud/pnpm/config/env-replace) | build input / potential shipped browser code; documentation web app |
| npm | @pnpm/network.ca-file | 1.0.2 | MIT | [project](https://bit.dev/pnpm/network/ca-file) | build input / potential shipped browser code; documentation web app |
| npm | @pnpm/npm-conf | 3.0.3 | MIT | [project](pnpm/npm-conf) | build input / potential shipped browser code; documentation web app |
| npm | @polka/url | 1.0.0-next.29 | MIT | [project](lukeed/polka) | build input / potential shipped browser code; documentation web app |
| npm | @rolldown/pluginutils | 1.0.0-rc.3 | MIT | [project](https://rolldown.rs/) | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-android-arm-eabi | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-android-arm-eabi | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-android-arm64 | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-android-arm64 | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-darwin-arm64 | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-darwin-arm64 | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-darwin-x64 | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-darwin-x64 | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-freebsd-arm64 | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-freebsd-arm64 | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-freebsd-x64 | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-freebsd-x64 | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-linux-arm-gnueabihf | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-linux-arm-gnueabihf | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-linux-arm-musleabihf | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-linux-arm-musleabihf | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-linux-arm64-gnu | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-linux-arm64-gnu | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-linux-arm64-musl | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-linux-arm64-musl | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-linux-loong64-gnu | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-linux-loong64-gnu | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-linux-loong64-musl | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-linux-loong64-musl | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-linux-ppc64-gnu | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-linux-ppc64-gnu | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-linux-ppc64-musl | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-linux-ppc64-musl | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-linux-riscv64-gnu | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-linux-riscv64-gnu | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-linux-riscv64-musl | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-linux-riscv64-musl | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-linux-s390x-gnu | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-linux-s390x-gnu | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-linux-x64-gnu | 4.62.2 | MIT | [project](https://rollupjs.org/) | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-linux-x64-gnu | 4.62.4 | MIT | [project](https://rollupjs.org/) | build/dev only; work dashboard |
| npm | @rollup/rollup-linux-x64-musl | 4.62.2 | MIT | [project](https://rollupjs.org/) | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-linux-x64-musl | 4.62.4 | MIT | [project](https://rollupjs.org/) | build/dev only; work dashboard |
| npm | @rollup/rollup-openbsd-x64 | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-openbsd-x64 | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-openharmony-arm64 | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-openharmony-arm64 | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-win32-arm64-msvc | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-win32-arm64-msvc | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-win32-ia32-msvc | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-win32-ia32-msvc | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-win32-x64-gnu | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-win32-x64-gnu | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @rollup/rollup-win32-x64-msvc | 4.62.2 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | @rollup/rollup-win32-x64-msvc | 4.62.4 | MIT | **Unresolved** | build/dev only; work dashboard |
| npm | @sideway/address | 4.1.5 | BSD-3-Clause | [project](https://github.com/sideway/address) | build input / potential shipped browser code; documentation web app |
| npm | @sideway/formula | 3.0.1 | BSD-3-Clause | [project](https://github.com/sideway/formula) | build input / potential shipped browser code; documentation web app |
| npm | @sideway/pinpoint | 2.0.0 | BSD-3-Clause | [project](https://github.com/sideway/pinpoint) | build input / potential shipped browser code; documentation web app |
| npm | @sinclair/typebox | 0.27.10 | MIT | [project](https://github.com/sinclairzx81/typebox-legacy) | build input / potential shipped browser code; documentation web app |
| npm | @sindresorhus/is | 4.6.0 | MIT | [project](sindresorhus/is) | build input / potential shipped browser code; documentation web app |
| npm | @sindresorhus/is | 5.6.0 | MIT | [project](sindresorhus/is) | build input / potential shipped browser code; documentation web app |
| npm | @slorber/react-helmet-async | 1.3.0 | Apache-2.0 | [project](http://github.com/staylor/react-helmet-async) | build input / potential shipped browser code; documentation web app |
| npm | @slorber/remark-comment | 1.0.0 | MIT | [project](https://github.com/leebyron/remark-comment) | build input / potential shipped browser code; documentation web app |
| npm | @standard-schema/spec | 1.1.0 | MIT | [project](https://standardschema.dev) | build/dev only; embedded admin app |
| npm | @svgr/babel-plugin-add-jsx-attribute | 8.0.0 | MIT | [project](https://react-svgr.com) | build input / potential shipped browser code; documentation web app |
| npm | @svgr/babel-plugin-remove-jsx-attribute | 8.0.0 | MIT | [project](https://react-svgr.com) | build input / potential shipped browser code; documentation web app |
| npm | @svgr/babel-plugin-remove-jsx-empty-expression | 8.0.0 | MIT | [project](https://react-svgr.com) | build input / potential shipped browser code; documentation web app |
| npm | @svgr/babel-plugin-replace-jsx-attribute-value | 8.0.0 | MIT | [project](https://react-svgr.com) | build input / potential shipped browser code; documentation web app |
| npm | @svgr/babel-plugin-svg-dynamic-title | 8.0.0 | MIT | [project](https://react-svgr.com) | build input / potential shipped browser code; documentation web app |
| npm | @svgr/babel-plugin-svg-em-dimensions | 8.0.0 | MIT | [project](https://react-svgr.com) | build input / potential shipped browser code; documentation web app |
| npm | @svgr/babel-plugin-transform-react-native-svg | 8.1.0 | MIT | [project](https://react-svgr.com) | build input / potential shipped browser code; documentation web app |
| npm | @svgr/babel-plugin-transform-svg-component | 8.0.0 | MIT | [project](https://react-svgr.com) | build input / potential shipped browser code; documentation web app |
| npm | @svgr/babel-preset | 8.1.0 | MIT | [project](https://react-svgr.com) | build input / potential shipped browser code; documentation web app |
| npm | @svgr/core | 8.1.0 | MIT | [project](https://react-svgr.com) | build input / potential shipped browser code; documentation web app |
| npm | @svgr/hast-util-to-babel-ast | 8.0.0 | MIT | [project](https://react-svgr.com) | build input / potential shipped browser code; documentation web app |
| npm | @svgr/plugin-jsx | 8.1.0 | MIT | [project](https://react-svgr.com) | build input / potential shipped browser code; documentation web app |
| npm | @svgr/plugin-svgo | 8.1.0 | MIT | [project](https://react-svgr.com) | build input / potential shipped browser code; documentation web app |
| npm | @svgr/webpack | 8.1.0 | MIT | [project](https://react-svgr.com) | build input / potential shipped browser code; documentation web app |
| npm | @swc/helpers | 0.5.23 | Apache-2.0 | [project](https://swc.rs) | development/operator tool; work dashboard |
| npm | @szmarczak/http-timer | 5.0.1 | MIT | [project](https://github.com/szmarczak/http-timer#readme) | build input / potential shipped browser code; documentation web app |
| npm | @types/babel__core | 7.20.5 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/babel__core) | shipped binary/web artifact; embedded admin app |
| npm | @types/babel__generator | 7.27.0 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/babel__generator) | shipped binary/web artifact; embedded admin app |
| npm | @types/babel__template | 7.4.4 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/babel__template) | shipped binary/web artifact; embedded admin app |
| npm | @types/babel__traverse | 7.28.0 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/babel__traverse) | shipped binary/web artifact; embedded admin app |
| npm | @types/body-parser | 1.19.6 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/body-parser) | build input / potential shipped browser code; documentation web app |
| npm | @types/bonjour | 3.5.13 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/bonjour) | build input / potential shipped browser code; documentation web app |
| npm | @types/chai | 5.2.3 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/chai) | build/dev only; embedded admin app |
| npm | @types/command-line-args | 5.2.3 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/command-line-args) | development/operator tool; work dashboard |
| npm | @types/command-line-usage | 5.0.4 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/command-line-usage) | development/operator tool; work dashboard |
| npm | @types/connect-history-api-fallback | 1.5.4 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/connect-history-api-fallback) | build input / potential shipped browser code; documentation web app |
| npm | @types/connect | 3.4.38 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/connect) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-array | 3.2.2 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-array) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-axis | 3.0.6 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-axis) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-brush | 3.0.6 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-brush) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-chord | 3.0.6 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-chord) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-color | 3.1.3 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-color) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-contour | 3.0.6 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-contour) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-delaunay | 6.0.4 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-delaunay) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-dispatch | 3.0.7 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-dispatch) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-drag | 3.0.7 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-drag) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-dsv | 3.0.7 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-dsv) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-ease | 3.0.2 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-ease) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-fetch | 3.0.7 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-fetch) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-force | 3.0.10 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-force) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-format | 3.0.4 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-format) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-geo | 3.1.0 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-geo) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-hierarchy | 3.1.7 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-hierarchy) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-interpolate | 3.0.4 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-interpolate) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-path | 3.1.1 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-path) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-polygon | 3.0.2 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-polygon) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-quadtree | 3.0.6 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-quadtree) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-random | 3.0.3 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-random) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-scale-chromatic | 3.1.0 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-scale-chromatic) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-scale | 4.0.9 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-scale) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-selection | 3.0.11 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-selection) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-shape | 3.1.8 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-shape) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-time-format | 4.0.3 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-time-format) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-time | 3.0.4 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-time) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-timer | 3.0.2 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-timer) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-transition | 3.0.9 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-transition) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3-zoom | 3.0.8 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3-zoom) | build input / potential shipped browser code; documentation web app |
| npm | @types/d3 | 7.4.3 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/d3) | build input / potential shipped browser code; documentation web app |
| npm | @types/debug | 4.1.13 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/debug) | build input / potential shipped browser code; documentation web app |
| npm | @types/deep-eql | 4.0.2 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/deep-eql) | build/dev only; embedded admin app |
| npm | @types/estree-jsx | 1.0.5 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/estree-jsx) | build input / potential shipped browser code; documentation web app |
| npm | @types/estree | 1.0.9 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/estree) | build input / potential shipped browser code, shipped binary/web artifact, build/dev only; documentation web app, embedded admin app, work dashboard |
| npm | @types/express-serve-static-core | 4.19.8 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/express-serve-static-core) | build input / potential shipped browser code; documentation web app |
| npm | @types/express | 4.17.25 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/express) | build input / potential shipped browser code; documentation web app |
| npm | @types/geojson | 7946.0.16 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/geojson) | build input / potential shipped browser code; documentation web app |
| npm | @types/gtag.js | 0.0.20 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/gtag.js) | build input / potential shipped browser code; documentation web app |
| npm | @types/hast | 3.0.4 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/hast) | build input / potential shipped browser code; documentation web app |
| npm | @types/history | 4.7.11 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/history) | build input / potential shipped browser code; documentation web app |
| npm | @types/html-minifier-terser | 6.1.0 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/html-minifier-terser) | build input / potential shipped browser code; documentation web app |
| npm | @types/http-cache-semantics | 4.2.0 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/http-cache-semantics) | build input / potential shipped browser code; documentation web app |
| npm | @types/http-errors | 2.0.5 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/http-errors) | build input / potential shipped browser code; documentation web app |
| npm | @types/http-proxy | 1.17.17 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/http-proxy) | build input / potential shipped browser code; documentation web app |
| npm | @types/istanbul-lib-coverage | 2.0.6 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/istanbul-lib-coverage) | build input / potential shipped browser code; documentation web app |
| npm | @types/istanbul-lib-report | 3.0.3 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/istanbul-lib-report) | build input / potential shipped browser code; documentation web app |
| npm | @types/istanbul-reports | 3.0.4 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/istanbul-reports) | build input / potential shipped browser code; documentation web app |
| npm | @types/json-schema | 7.0.15 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/json-schema) | build input / potential shipped browser code; documentation web app |
| npm | @types/mdast | 4.0.4 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/mdast) | build input / potential shipped browser code; documentation web app |
| npm | @types/mdx | 2.0.14 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/mdx) | build input / potential shipped browser code; documentation web app |
| npm | @types/mime | 1.3.5 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/mime) | build input / potential shipped browser code; documentation web app |
| npm | @types/ms | 2.1.0 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/ms) | build input / potential shipped browser code; documentation web app |
| npm | @types/node | 17.0.45 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/node) | build input / potential shipped browser code; documentation web app |
| npm | @types/node | 20.19.43 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/node) | development/operator tool; work dashboard |
| npm | @types/node | 25.9.3 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/node) | build input / potential shipped browser code; documentation web app |
| npm | @types/node | 25.9.4 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/node) | shipped binary/web artifact; embedded admin app |
| npm | @types/prismjs | 1.26.6 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/prismjs) | build input / potential shipped browser code; documentation web app |
| npm | @types/qs | 6.15.1 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/qs) | build input / potential shipped browser code; documentation web app |
| npm | @types/range-parser | 1.2.7 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/range-parser) | build input / potential shipped browser code; documentation web app |
| npm | @types/react-dom | 19.2.3 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/react-dom) | build/dev only; embedded admin app |
| npm | @types/react-router-config | 5.0.11 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/react-router-config) | build input / potential shipped browser code; documentation web app |
| npm | @types/react-router-dom | 5.3.3 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/react-router-dom) | build input / potential shipped browser code; documentation web app |
| npm | @types/react-router | 5.1.20 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/react-router) | build input / potential shipped browser code; documentation web app |
| npm | @types/react | 19.2.17 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/react) | build input / potential shipped browser code, build/dev only; documentation web app, embedded admin app |
| npm | @types/retry | 0.12.2 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/retry) | build input / potential shipped browser code; documentation web app |
| npm | @types/sax | 1.2.7 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/sax) | build input / potential shipped browser code; documentation web app |
| npm | @types/send | 0.17.6 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/send) | build input / potential shipped browser code; documentation web app |
| npm | @types/send | 1.2.1 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/send) | build input / potential shipped browser code; documentation web app |
| npm | @types/serve-index | 1.9.4 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/serve-index) | build input / potential shipped browser code; documentation web app |
| npm | @types/serve-static | 1.15.10 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/serve-static) | build input / potential shipped browser code; documentation web app |
| npm | @types/sockjs | 0.3.36 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/sockjs) | build input / potential shipped browser code; documentation web app |
| npm | @types/trusted-types | 2.0.7 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/trusted-types) | build input / potential shipped browser code; documentation web app |
| npm | @types/unist | 2.0.11 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/unist) | build input / potential shipped browser code; documentation web app |
| npm | @types/unist | 3.0.3 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/unist) | build input / potential shipped browser code; documentation web app |
| npm | @types/ws | 8.18.1 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/ws) | build input / potential shipped browser code; documentation web app |
| npm | @types/yargs-parser | 21.0.3 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/yargs-parser) | build input / potential shipped browser code; documentation web app |
| npm | @types/yargs | 17.0.35 | MIT | [project](https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/yargs) | build input / potential shipped browser code; documentation web app |
| npm | @ungap/structured-clone | 1.3.1 | ISC | [project](https://github.com/ungap/structured-clone#readme) | build input / potential shipped browser code; documentation web app |
| npm | @upsetjs/venn.js | 2.0.0 | MIT | [project](https://github.com/upsetjs/venn.js) | build input / potential shipped browser code; documentation web app |
| npm | @uwdata/flechette | 2.5.0 | BSD-3-Clause | [project](https://github.com/uwdata/flechette) | development/operator tool; work dashboard |
| npm | @uwdata/mosaic-core | 0.29.2 | BSD-3-Clause | [project](https://github.com/uwdata/mosaic) | development/operator tool; work dashboard |
| npm | @uwdata/mosaic-inputs | 0.29.2 | BSD-3-Clause | [project](https://github.com/uwdata/mosaic) | development/operator tool; work dashboard |
| npm | @uwdata/mosaic-plot | 0.29.2 | BSD-3-Clause | [project](https://github.com/uwdata/mosaic) | development/operator tool; work dashboard |
| npm | @uwdata/mosaic-sql | 0.29.0 | BSD-3-Clause | [project](https://github.com/uwdata/mosaic) | development/operator tool; work dashboard |
| npm | @uwdata/vgplot | 0.29.2 | BSD-3-Clause | [project](https://github.com/uwdata/mosaic) | development/operator tool; work dashboard |
| npm | @vitejs/plugin-react | 5.2.0 | MIT | [project](https://github.com/vitejs/vite-plugin-react/tree/main/packages/plugin-react#readme) | shipped binary/web artifact; embedded admin app |
| npm | @vitest/expect | 4.1.9 | MIT | [project](https://vitest.dev/api/expect) | build/dev only; embedded admin app |
| npm | @vitest/mocker | 4.1.9 | MIT | [project](https://github.com/vitest-dev/vitest/tree/main/packages/mocker) | build/dev only; embedded admin app |
| npm | @vitest/pretty-format | 4.1.9 | MIT | [project](https://github.com/vitest-dev/vitest/tree/main/packages/pretty-format) | build/dev only; embedded admin app |
| npm | @vitest/runner | 4.1.9 | MIT | [project](https://vitest.dev/api/advanced/runner) | build/dev only; embedded admin app |
| npm | @vitest/snapshot | 4.1.9 | MIT | [project](https://vitest.dev/guide/snapshot) | build/dev only; embedded admin app |
| npm | @vitest/spy | 4.1.9 | MIT | [project](https://vitest.dev/api/mock) | build/dev only; embedded admin app |
| npm | @vitest/utils | 4.1.9 | MIT | [project](https://github.com/vitest-dev/vitest/tree/main/packages/utils) | build/dev only; embedded admin app |
| npm | @webassemblyjs/ast | 1.14.1 | MIT | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @webassemblyjs/floating-point-hex-parser | 1.13.2 | MIT | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @webassemblyjs/helper-api-error | 1.13.2 | MIT | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @webassemblyjs/helper-buffer | 1.14.1 | MIT | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @webassemblyjs/helper-numbers | 1.13.2 | MIT | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @webassemblyjs/helper-wasm-bytecode | 1.13.2 | MIT | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @webassemblyjs/helper-wasm-section | 1.14.1 | MIT | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @webassemblyjs/ieee754 | 1.13.2 | MIT | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @webassemblyjs/leb128 | 1.13.2 | Apache-2.0 | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @webassemblyjs/utf8 | 1.13.2 | MIT | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @webassemblyjs/wasm-edit | 1.14.1 | MIT | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @webassemblyjs/wasm-gen | 1.14.1 | MIT | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @webassemblyjs/wasm-opt | 1.14.1 | MIT | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @webassemblyjs/wasm-parser | 1.14.1 | MIT | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @webassemblyjs/wast-printer | 1.14.1 | MIT | [project](https://github.com/xtuc/webassemblyjs) | build input / potential shipped browser code; documentation web app |
| npm | @xtuc/ieee754 | 1.2.0 | BSD-3-Clause | [project](https://github.com/feross/ieee754) | build input / potential shipped browser code; documentation web app |
| npm | @xtuc/long | 4.2.2 | Apache-2.0 | [project](https://github.com/dcodeIO/long.js) | build input / potential shipped browser code; documentation web app |
| npm | accepts | 1.3.8 | MIT | [project](jshttp/accepts) | build input / potential shipped browser code; documentation web app |
| npm | acorn-import-phases | 1.0.4 | MIT | [project](https://github.com/nicolo-ribaudo/acorn-import-phases) | build input / potential shipped browser code; documentation web app |
| npm | acorn-jsx | 5.3.2 | MIT | [project](https://github.com/acornjs/acorn-jsx) | build input / potential shipped browser code; documentation web app |
| npm | acorn-walk | 8.3.5 | MIT | [project](https://github.com/acornjs/acorn) | build input / potential shipped browser code; documentation web app |
| npm | acorn | 8.17.0 | MIT | [project](https://github.com/acornjs/acorn) | build input / potential shipped browser code; documentation web app |
| npm | address | 1.2.2 | MIT | [project](https://github.com/node-modules/address) | build input / potential shipped browser code; documentation web app |
| npm | aggregate-error | 3.1.0 | MIT | [project](sindresorhus/aggregate-error) | build input / potential shipped browser code; documentation web app |
| npm | ajv-formats | 2.1.1 | MIT | [project](https://github.com/ajv-validator/ajv-formats#readme) | build input / potential shipped browser code; documentation web app |
| npm | ajv-keywords | 3.5.2 | MIT | [project](https://github.com/epoberezkin/ajv-keywords#readme) | build input / potential shipped browser code; documentation web app |
| npm | ajv-keywords | 5.1.0 | MIT | [project](https://github.com/epoberezkin/ajv-keywords#readme) | build input / potential shipped browser code; documentation web app |
| npm | ajv | 6.15.0 | MIT | [project](https://github.com/ajv-validator/ajv) | build input / potential shipped browser code; documentation web app |
| npm | ajv | 8.20.0 | MIT | [project](https://ajv.js.org) | build input / potential shipped browser code; documentation web app |
| npm | algoliasearch-helper | 3.29.1 | MIT | [project](https://community.algolia.com/algoliasearch-helper-js/) | build input / potential shipped browser code; documentation web app |
| npm | algoliasearch | 5.54.0 | MIT | [project](https://github.com/algolia/algoliasearch-client-javascript/tree/main/packages/algoliasearch#readme) | build input / potential shipped browser code; documentation web app |
| npm | ansi-align | 3.0.1 | ISC | [project](https://github.com/nexdrew/ansi-align#readme) | build input / potential shipped browser code; documentation web app |
| npm | ansi-html-community | 0.0.8 | Apache-2.0 | [project](https://github.com/mahdyar/ansi-html-community) | build input / potential shipped browser code; documentation web app |
| npm | ansi-regex | 5.0.1 | MIT | [project](chalk/ansi-regex) | build input / potential shipped browser code; documentation web app |
| npm | ansi-regex | 6.2.2 | MIT | [project](chalk/ansi-regex) | build input / potential shipped browser code; documentation web app |
| npm | ansi-styles | 4.3.0 | MIT | [project](chalk/ansi-styles) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | ansi-styles | 6.2.3 | MIT | [project](chalk/ansi-styles) | build input / potential shipped browser code; documentation web app |
| npm | ansis | 3.17.0 | ISC | [project](webdiscus/ansis) | build input / potential shipped browser code; documentation web app |
| npm | any-promise | 1.3.0 | MIT | [project](http://github.com/kevinbeaty/any-promise) | shipped binary/web artifact; embedded admin app |
| npm | anymatch | 3.1.3 | ISC | [project](https://github.com/micromatch/anymatch) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | apache-arrow | 17.0.0 | Apache-2.0 | [project](https://github.com/apache/arrow/blob/main/js/README.md) | development/operator tool; work dashboard |
| npm | arg | 5.0.2 | MIT | [project](vercel/arg) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | argparse | 1.0.10 | MIT | [project](nodeca/argparse) | build input / potential shipped browser code; documentation web app |
| npm | argparse | 2.0.1 | Python-2.0 | [project](nodeca/argparse) | build input / potential shipped browser code; documentation web app |
| npm | array-back | 3.1.0 | MIT | [project](https://github.com/75lb/array-back) | development/operator tool; work dashboard |
| npm | array-back | 6.2.3 | MIT | [project](https://github.com/75lb/array-back) | development/operator tool; work dashboard |
| npm | array-flatten | 1.1.1 | MIT | [project](https://github.com/blakeembrey/array-flatten) | build input / potential shipped browser code; documentation web app |
| npm | array-union | 2.1.0 | MIT | [project](sindresorhus/array-union) | build input / potential shipped browser code; documentation web app |
| npm | asn1js | 3.0.10 | BSD-3-Clause | [project](https://github.com/PeculiarVentures/ASN1.js) | build input / potential shipped browser code; documentation web app |
| npm | assertion-error | 2.0.1 | MIT | [project](https://github.com/chaijs/assertion-error) | build/dev only; embedded admin app |
| npm | astring | 1.9.0 | MIT | [project](https://github.com/davidbonnet/astring) | build input / potential shipped browser code; documentation web app |
| npm | autoprefixer | 10.5.0 | MIT | [project](postcss/autoprefixer) | build input / potential shipped browser code; documentation web app |
| npm | autoprefixer | 10.5.2 | MIT | [project](postcss/autoprefixer) | build/dev only; embedded admin app |
| npm | babel-loader | 9.2.1 | MIT | [project](https://github.com/babel/babel-loader) | build input / potential shipped browser code; documentation web app |
| npm | babel-plugin-dynamic-import-node | 2.3.3 | MIT | [project](https://github.com/airbnb/babel-plugin-dynamic-import-node#readme) | build input / potential shipped browser code; documentation web app |
| npm | babel-plugin-polyfill-corejs2 | 0.4.17 | MIT | [project](https://github.com/babel/babel-polyfills) | build input / potential shipped browser code; documentation web app |
| npm | babel-plugin-polyfill-corejs3 | 0.13.0 | MIT | [project](https://github.com/babel/babel-polyfills) | build input / potential shipped browser code; documentation web app |
| npm | babel-plugin-polyfill-corejs3 | 0.14.2 | MIT | [project](https://github.com/babel/babel-polyfills) | build input / potential shipped browser code; documentation web app |
| npm | babel-plugin-polyfill-regenerator | 0.6.8 | MIT | [project](https://github.com/babel/babel-polyfills) | build input / potential shipped browser code; documentation web app |
| npm | bail | 2.0.2 | MIT | [project](wooorm/bail) | build input / potential shipped browser code; documentation web app |
| npm | balanced-match | 1.0.2 | MIT | [project](https://github.com/juliangruber/balanced-match) | build input / potential shipped browser code; documentation web app |
| npm | baseline-browser-mapping | 2.10.37 | Apache-2.0 | [project](https://github.com/web-platform-dx/baseline-browser-mapping) | build input / potential shipped browser code; documentation web app |
| npm | baseline-browser-mapping | 2.10.40 | Apache-2.0 | [project](https://github.com/web-platform-dx/baseline-browser-mapping) | shipped binary/web artifact; embedded admin app |
| npm | batch | 0.6.1 | MIT | [project](https://github.com/visionmedia/batch) | build input / potential shipped browser code; documentation web app |
| npm | big.js | 5.2.2 | MIT | [project](https://github.com/MikeMcl/big.js) | build input / potential shipped browser code; documentation web app |
| npm | binary-extensions | 2.3.0 | MIT | [project](sindresorhus/binary-extensions) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | binary-search-bounds | 2.0.5 | MIT | [project](https://github.com/mikolalysenko/binary-search-bounds) | development/operator tool; work dashboard |
| npm | body-parser | 1.20.5 | MIT | [project](expressjs/body-parser) | build input / potential shipped browser code; documentation web app |
| npm | bonjour-service | 1.4.1 | MIT | [project](https://github.com/onlxltd/bonjour-service) | build input / potential shipped browser code; documentation web app |
| npm | boolbase | 1.0.0 | ISC | [project](https://github.com/fb55/boolbase) | build input / potential shipped browser code; documentation web app |
| npm | boxen | 6.2.1 | MIT | [project](sindresorhus/boxen) | build input / potential shipped browser code; documentation web app |
| npm | boxen | 7.1.1 | MIT | [project](sindresorhus/boxen) | build input / potential shipped browser code; documentation web app |
| npm | brace-expansion | 1.1.15 | MIT | [project](https://github.com/juliangruber/brace-expansion) | build input / potential shipped browser code; documentation web app |
| npm | braces | 3.0.3 | MIT | [project](https://github.com/micromatch/braces) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | browserslist | 4.28.2 | MIT | [project](browserslist/browserslist) | build input / potential shipped browser code; documentation web app |
| npm | browserslist | 4.28.4 | MIT | [project](browserslist/browserslist) | shipped binary/web artifact; embedded admin app |
| npm | buffer-from | 1.1.2 | MIT | [project](LinusU/buffer-from) | build input / potential shipped browser code; documentation web app |
| npm | bundle-name | 4.1.0 | MIT | [project](sindresorhus/bundle-name) | build input / potential shipped browser code; documentation web app |
| npm | bytes | 3.0.0 | MIT | [project](visionmedia/bytes.js) | build input / potential shipped browser code; documentation web app |
| npm | bytes | 3.1.2 | MIT | [project](visionmedia/bytes.js) | build input / potential shipped browser code; documentation web app |
| npm | bytestreamjs | 2.0.1 | BSD-3-Clause | [project](https://github.com/PeculiarVentures/ByteStream.js) | build input / potential shipped browser code; documentation web app |
| npm | cacheable-lookup | 7.0.0 | MIT | [project](https://github.com/szmarczak/cacheable-lookup#readme) | build input / potential shipped browser code; documentation web app |
| npm | cacheable-request | 10.2.14 | MIT | [project](jaredwray/cacheable) | build input / potential shipped browser code; documentation web app |
| npm | call-bind-apply-helpers | 1.0.2 | MIT | [project](https://github.com/ljharb/call-bind-apply-helpers#readme) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | call-bind | 1.0.9 | MIT | [project](https://github.com/ljharb/call-bind#readme) | build input / potential shipped browser code; documentation web app |
| npm | call-bound | 1.0.4 | MIT | [project](https://github.com/ljharb/call-bound#readme) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | callsites | 3.1.0 | MIT | [project](sindresorhus/callsites) | build input / potential shipped browser code; documentation web app |
| npm | camel-case | 4.1.2 | MIT | [project](https://github.com/blakeembrey/change-case/tree/master/packages/camel-case#readme) | build input / potential shipped browser code; documentation web app |
| npm | camelcase-css | 2.0.1 | MIT | [project](stevenvachon/camelcase-css) | shipped binary/web artifact; embedded admin app |
| npm | camelcase | 6.3.0 | MIT | [project](sindresorhus/camelcase) | build input / potential shipped browser code; documentation web app |
| npm | camelcase | 7.0.1 | MIT | [project](sindresorhus/camelcase) | build input / potential shipped browser code; documentation web app |
| npm | caniuse-api | 3.0.0 | MIT | [project](https://github.com/nyalab/caniuse-api) | build input / potential shipped browser code; documentation web app |
| npm | caniuse-lite | 1.0.30001799 | CC-BY-4.0 | [project](browserslist/caniuse-lite) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | ccount | 2.0.1 | MIT | [project](wooorm/ccount) | build input / potential shipped browser code; documentation web app |
| npm | chai | 6.2.2 | MIT | [project](http://chaijs.com) | build/dev only; embedded admin app |
| npm | chalk-template | 0.4.0 | MIT | [project](chalk/chalk-template) | development/operator tool; work dashboard |
| npm | chalk | 4.1.2 | MIT | [project](chalk/chalk) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | chalk | 5.6.2 | MIT | [project](chalk/chalk) | build input / potential shipped browser code; documentation web app |
| npm | char-regex | 1.0.2 | MIT | [project](https://github.com/Richienb/char-regex) | build input / potential shipped browser code; documentation web app |
| npm | character-entities-html4 | 2.1.0 | MIT | [project](wooorm/character-entities-html4) | build input / potential shipped browser code; documentation web app |
| npm | character-entities-legacy | 3.0.0 | MIT | [project](wooorm/character-entities-legacy) | build input / potential shipped browser code; documentation web app |
| npm | character-entities | 2.0.2 | MIT | [project](wooorm/character-entities) | build input / potential shipped browser code; documentation web app |
| npm | character-reference-invalid | 2.0.1 | MIT | [project](wooorm/character-reference-invalid) | build input / potential shipped browser code; documentation web app |
| npm | chart.js | 4.5.1 | MIT | [project](https://www.chartjs.org) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | cheerio-select | 2.1.0 | BSD-2-Clause | [project](https://github.com/cheeriojs/cheerio-select) | build input / potential shipped browser code; documentation web app |
| npm | cheerio | 1.0.0-rc.12 | MIT | [project](https://cheerio.js.org/) | build input / potential shipped browser code; documentation web app |
| npm | chokidar | 3.6.0 | MIT | [project](https://github.com/paulmillr/chokidar) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | chrome-trace-event | 1.0.4 | MIT | [project](github:samccone/chrome-trace-event) | build input / potential shipped browser code; documentation web app |
| npm | ci-info | 3.9.0 | MIT | [project](https://github.com/watson/ci-info) | build input / potential shipped browser code; documentation web app |
| npm | class-variance-authority | 0.7.1 | Apache-2.0 | [project](https://github.com/joe-bell/cva#readme) | shipped binary/web artifact; embedded admin app |
| npm | clean-css | 5.3.3 | MIT | [project](https://github.com/clean-css/clean-css) | build input / potential shipped browser code; documentation web app |
| npm | clean-stack | 2.2.0 | MIT | [project](sindresorhus/clean-stack) | build input / potential shipped browser code; documentation web app |
| npm | cli-boxes | 3.0.0 | MIT | [project](sindresorhus/cli-boxes) | build input / potential shipped browser code; documentation web app |
| npm | cli-table3 | 0.6.5 | MIT | [project](https://github.com/cli-table/cli-table3) | build input / potential shipped browser code; documentation web app |
| npm | clone-deep | 4.0.1 | MIT | [project](https://github.com/jonschlinkert/clone-deep) | build input / potential shipped browser code; documentation web app |
| npm | clsx | 2.1.1 | MIT | [project](lukeed/clsx) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | collapse-white-space | 2.1.0 | MIT | [project](wooorm/collapse-white-space) | build input / potential shipped browser code; documentation web app |
| npm | color-convert | 2.0.1 | MIT | [project](Qix-/color-convert) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | color-name | 1.1.4 | MIT | [project](https://github.com/colorjs/color-name) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | colord | 2.9.3 | MIT | [project](omgovich/colord) | build input / potential shipped browser code; documentation web app |
| npm | colorette | 2.0.20 | MIT | [project](jorgebucaran/colorette) | build input / potential shipped browser code; documentation web app |
| npm | combine-promises | 1.2.0 | MIT | [project](https://github.com/slorber/combine-promises) | build input / potential shipped browser code; documentation web app |
| npm | comma-separated-tokens | 2.0.3 | MIT | [project](wooorm/comma-separated-tokens) | build input / potential shipped browser code; documentation web app |
| npm | command-line-args | 5.2.1 | MIT | [project](https://github.com/75lb/command-line-args) | development/operator tool; work dashboard |
| npm | command-line-usage | 7.0.4 | MIT | [project](https://github.com/75lb/command-line-usage) | development/operator tool; work dashboard |
| npm | commander | 10.0.1 | MIT | [project](https://github.com/tj/commander.js) | build input / potential shipped browser code; documentation web app |
| npm | commander | 2.20.3 | MIT | [project](https://github.com/tj/commander.js) | build input / potential shipped browser code; documentation web app |
| npm | commander | 4.1.1 | MIT | [project](https://github.com/tj/commander.js) | shipped binary/web artifact; embedded admin app |
| npm | commander | 5.1.0 | MIT | [project](https://github.com/tj/commander.js) | build input / potential shipped browser code; documentation web app |
| npm | commander | 7.2.0 | MIT | [project](https://github.com/tj/commander.js) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | commander | 8.3.0 | MIT | [project](https://github.com/tj/commander.js) | build input / potential shipped browser code; documentation web app |
| npm | common-path-prefix | 3.0.0 | ISC | [project](https://github.com/novemberborn/common-path-prefix#readme) | build input / potential shipped browser code; documentation web app |
| npm | compressible | 2.0.18 | MIT | [project](jshttp/compressible) | build input / potential shipped browser code; documentation web app |
| npm | compression | 1.8.1 | MIT | [project](expressjs/compression) | build input / potential shipped browser code; documentation web app |
| npm | concat-map | 0.0.1 | MIT | [project](https://github.com/substack/node-concat-map) | build input / potential shipped browser code; documentation web app |
| npm | config-chain | 1.1.13 | MIT | [project](http://github.com/dominictarr/config-chain) | build input / potential shipped browser code; documentation web app |
| npm | configstore | 6.0.0 | BSD-2-Clause | [project](yeoman/configstore) | build input / potential shipped browser code; documentation web app |
| npm | connect-history-api-fallback | 2.0.0 | MIT | [project](http://github.com/bripkens/connect-history-api-fallback) | build input / potential shipped browser code; documentation web app |
| npm | consola | 3.4.2 | MIT | [project](unjs/consola) | build input / potential shipped browser code; documentation web app |
| npm | content-disposition | 0.5.2 | MIT | [project](jshttp/content-disposition) | build input / potential shipped browser code; documentation web app |
| npm | content-disposition | 0.5.4 | MIT | [project](jshttp/content-disposition) | build input / potential shipped browser code; documentation web app |
| npm | content-type | 1.0.5 | MIT | [project](jshttp/content-type) | build input / potential shipped browser code; documentation web app |
| npm | convert-source-map | 2.0.0 | MIT | [project](https://github.com/thlorenz/convert-source-map) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | cookie-signature | 1.0.7 | MIT | [project](https://github.com/visionmedia/node-cookie-signature) | build input / potential shipped browser code; documentation web app |
| npm | cookie | 0.7.2 | MIT | [project](jshttp/cookie) | build input / potential shipped browser code; documentation web app |
| npm | copy-text-to-clipboard | 3.2.2 | MIT | [project](sindresorhus/copy-text-to-clipboard) | build input / potential shipped browser code; documentation web app |
| npm | copy-webpack-plugin | 11.0.0 | MIT | [project](https://github.com/webpack-contrib/copy-webpack-plugin) | build input / potential shipped browser code; documentation web app |
| npm | core-js-compat | 3.49.0 | MIT | [project](https://core-js.io) | build input / potential shipped browser code; documentation web app |
| npm | core-js | 3.49.0 | MIT | [project](https://core-js.io) | build input / potential shipped browser code; documentation web app |
| npm | core-util-is | 1.0.3 | MIT | [project](https://github.com/isaacs/core-util-is) | build input / potential shipped browser code; documentation web app |
| npm | cose-base | 1.0.3 | MIT | [project](https://github.com/iVis-at-Bilkent/cose-base#readme) | build input / potential shipped browser code; documentation web app |
| npm | cose-base | 2.2.0 | MIT | [project](https://github.com/iVis-at-Bilkent/cose-base#readme) | build input / potential shipped browser code; documentation web app |
| npm | cosmiconfig | 8.3.6 | MIT | [project](https://github.com/cosmiconfig/cosmiconfig#readme) | build input / potential shipped browser code; documentation web app |
| npm | cross-spawn | 7.0.6 | MIT | [project](https://github.com/moxystudio/node-cross-spawn) | build input / potential shipped browser code; documentation web app |
| npm | crypto-random-string | 4.0.0 | MIT | [project](sindresorhus/crypto-random-string) | build input / potential shipped browser code; documentation web app |
| npm | css-blank-pseudo | 7.0.1 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/css-blank-pseudo#readme) | build input / potential shipped browser code; documentation web app |
| npm | css-declaration-sorter | 7.4.0 | ISC | [project](https://github.com/Siilwyn/css-declaration-sorter) | build input / potential shipped browser code; documentation web app |
| npm | css-has-pseudo | 7.0.3 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/css-has-pseudo#readme) | build input / potential shipped browser code; documentation web app |
| npm | css-loader | 6.11.0 | MIT | [project](https://github.com/webpack-contrib/css-loader) | build input / potential shipped browser code; documentation web app |
| npm | css-minimizer-webpack-plugin | 5.0.1 | MIT | [project](https://github.com/webpack-contrib/css-minimizer-webpack-plugin) | build input / potential shipped browser code; documentation web app |
| npm | css-prefers-color-scheme | 10.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/css-prefers-color-scheme#readme) | build input / potential shipped browser code; documentation web app |
| npm | css-select | 4.3.0 | BSD-2-Clause | [project](https://github.com/fb55/css-select) | build input / potential shipped browser code; documentation web app |
| npm | css-select | 5.2.2 | BSD-2-Clause | [project](https://github.com/fb55/css-select) | build input / potential shipped browser code; documentation web app |
| npm | css-tree | 2.2.1 | MIT | [project](csstree/csstree) | build input / potential shipped browser code; documentation web app |
| npm | css-tree | 2.3.1 | MIT | [project](csstree/csstree) | build input / potential shipped browser code; documentation web app |
| npm | css-what | 6.2.2 | BSD-2-Clause | [project](https://github.com/fb55/css-what) | build input / potential shipped browser code; documentation web app |
| npm | cssdb | 8.9.0 | MIT-0 | [project](https://github.com/csstools/cssdb#readme) | build input / potential shipped browser code; documentation web app |
| npm | cssesc | 3.0.0 | MIT | [project](https://mths.be/cssesc) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | cssnano-preset-advanced | 6.1.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | cssnano-preset-default | 6.1.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | cssnano-utils | 4.0.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | cssnano | 6.1.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | csso | 5.0.5 | MIT | [project](css/csso) | build input / potential shipped browser code; documentation web app |
| npm | csstype | 3.2.3 | MIT | [project](https://github.com/frenic/csstype) | build input / potential shipped browser code, build/dev only; documentation web app, embedded admin app |
| npm | cytoscape-cose-bilkent | 4.1.0 | MIT | [project](https://github.com/cytoscape/cytoscape.js-cose-bilkent) | build input / potential shipped browser code; documentation web app |
| npm | cytoscape-fcose | 2.2.0 | MIT | [project](https://github.com/iVis-at-Bilkent/cytoscape.js-fcose) | build input / potential shipped browser code; documentation web app |
| npm | cytoscape | 3.34.0 | MIT | [project](http://js.cytoscape.org) | build input / potential shipped browser code; documentation web app |
| npm | d3-array | 2.12.1 | BSD-3-Clause | [project](https://d3js.org/d3-array/) | build input / potential shipped browser code; documentation web app |
| npm | d3-array | 3.2.4 | ISC | [project](https://d3js.org/d3-array/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-axis | 3.0.0 | ISC | [project](https://d3js.org/d3-axis/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-brush | 3.0.0 | ISC | [project](https://d3js.org/d3-brush/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-chord | 3.0.1 | ISC | [project](https://d3js.org/d3-chord/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-color | 3.1.0 | ISC | [project](https://d3js.org/d3-color/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-contour | 4.0.2 | ISC | [project](https://d3js.org/d3-contour/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-delaunay | 6.0.4 | ISC | [project](https://github.com/d3/d3-delaunay) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-dispatch | 3.0.1 | ISC | [project](https://d3js.org/d3-dispatch/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-drag | 3.0.0 | ISC | [project](https://d3js.org/d3-drag/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-dsv | 3.0.1 | ISC | [project](https://d3js.org/d3-dsv/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-ease | 3.0.1 | BSD-3-Clause | [project](https://d3js.org/d3-ease/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-fetch | 3.0.1 | ISC | [project](https://d3js.org/d3-fetch/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-force | 3.0.0 | ISC | [project](https://d3js.org/d3-force/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-format | 3.1.2 | ISC | [project](https://d3js.org/d3-format/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-geo | 3.1.1 | ISC | [project](https://d3js.org/d3-geo/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-hierarchy | 3.1.2 | ISC | [project](https://d3js.org/d3-hierarchy/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-interpolate | 3.0.1 | ISC | [project](https://d3js.org/d3-interpolate/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-path | 1.0.9 | BSD-3-Clause | [project](https://d3js.org/d3-path/) | build input / potential shipped browser code; documentation web app |
| npm | d3-path | 3.1.0 | ISC | [project](https://d3js.org/d3-path/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-polygon | 3.0.1 | ISC | [project](https://d3js.org/d3-polygon/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-quadtree | 3.0.1 | ISC | [project](https://d3js.org/d3-quadtree/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-random | 3.0.1 | ISC | [project](https://d3js.org/d3-random/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-sankey | 0.12.3 | BSD-3-Clause | [project](https://github.com/d3/d3-sankey) | build input / potential shipped browser code; documentation web app |
| npm | d3-scale-chromatic | 3.1.0 | ISC | [project](https://d3js.org/d3-scale-chromatic/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-scale | 4.0.2 | ISC | [project](https://d3js.org/d3-scale/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-selection | 3.0.0 | ISC | [project](https://d3js.org/d3-selection/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-shape | 1.3.7 | BSD-3-Clause | [project](https://d3js.org/d3-shape/) | build input / potential shipped browser code; documentation web app |
| npm | d3-shape | 3.2.0 | ISC | [project](https://d3js.org/d3-shape/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-time-format | 4.1.0 | ISC | [project](https://d3js.org/d3-time-format/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-time | 3.1.0 | ISC | [project](https://d3js.org/d3-time/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-timer | 3.0.1 | ISC | [project](https://d3js.org/d3-timer/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-transition | 3.0.1 | ISC | [project](https://d3js.org/d3-transition/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3-zoom | 3.0.0 | ISC | [project](https://d3js.org/d3-zoom/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | d3 | 7.9.0 | ISC | [project](https://d3js.org) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | dagre-d3-es | 7.0.14 | MIT | [project](https://github.com/tbo47/dagre-es) | build input / potential shipped browser code; documentation web app |
| npm | dayjs | 1.11.21 | MIT | [project](https://day.js.org) | build input / potential shipped browser code; documentation web app |
| npm | debounce | 1.2.1 | MIT | [project](https://github.com/component/debounce) | build input / potential shipped browser code; documentation web app |
| npm | debug | 2.6.9 | MIT | [project](https://github.com/visionmedia/debug) | build input / potential shipped browser code; documentation web app |
| npm | debug | 4.4.3 | MIT | [project](https://github.com/debug-js/debug) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | decode-named-character-reference | 1.3.0 | MIT | [project](wooorm/decode-named-character-reference) | build input / potential shipped browser code; documentation web app |
| npm | decompress-response | 6.0.0 | MIT | [project](sindresorhus/decompress-response) | build input / potential shipped browser code; documentation web app |
| npm | deep-extend | 0.6.0 | MIT | [project](https://github.com/unclechu/node-deep-extend) | build input / potential shipped browser code; documentation web app |
| npm | deepmerge | 4.3.1 | MIT | [project](https://github.com/TehShrike/deepmerge) | build input / potential shipped browser code; documentation web app |
| npm | default-browser-id | 5.0.1 | MIT | [project](sindresorhus/default-browser-id) | build input / potential shipped browser code; documentation web app |
| npm | default-browser | 5.5.0 | MIT | [project](sindresorhus/default-browser) | build input / potential shipped browser code; documentation web app |
| npm | defer-to-connect | 2.0.1 | MIT | [project](https://github.com/szmarczak/defer-to-connect#readme) | build input / potential shipped browser code; documentation web app |
| npm | define-data-property | 1.1.4 | MIT | [project](https://github.com/ljharb/define-data-property#readme) | build input / potential shipped browser code; documentation web app |
| npm | define-lazy-prop | 2.0.0 | MIT | [project](sindresorhus/define-lazy-prop) | build input / potential shipped browser code; documentation web app |
| npm | define-lazy-prop | 3.0.0 | MIT | [project](sindresorhus/define-lazy-prop) | build input / potential shipped browser code; documentation web app |
| npm | define-properties | 1.2.1 | MIT | [project](https://github.com/ljharb/define-properties) | build input / potential shipped browser code; documentation web app |
| npm | delaunator | 5.1.0 | ISC | [project](https://github.com/mapbox/delaunator) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | depd | 1.1.2 | MIT | [project](dougwilson/nodejs-depd) | build input / potential shipped browser code; documentation web app |
| npm | depd | 2.0.0 | MIT | [project](dougwilson/nodejs-depd) | build input / potential shipped browser code; documentation web app |
| npm | dequal | 2.0.3 | MIT | [project](lukeed/dequal) | build input / potential shipped browser code; documentation web app |
| npm | destroy | 1.2.0 | MIT | [project](stream-utils/destroy) | build input / potential shipped browser code; documentation web app |
| npm | detect-node | 2.1.0 | MIT | [project](https://github.com/iliakan/detect-node) | build input / potential shipped browser code; documentation web app |
| npm | detect-port | 1.6.1 | MIT | [project](https://github.com/node-modules/detect-port) | build input / potential shipped browser code; documentation web app |
| npm | devlop | 1.1.0 | MIT | [project](wooorm/devlop) | build input / potential shipped browser code; documentation web app |
| npm | didyoumean | 1.2.2 | Apache-2.0 | [project](https://github.com/dcporter/didyoumean.js) | shipped binary/web artifact; embedded admin app |
| npm | dir-glob | 3.0.1 | MIT | [project](kevva/dir-glob) | build input / potential shipped browser code; documentation web app |
| npm | dlv | 1.1.3 | MIT | [project](developit/dlv) | shipped binary/web artifact; embedded admin app |
| npm | dns-packet | 5.6.1 | MIT | [project](https://github.com/mafintosh/dns-packet) | build input / potential shipped browser code; documentation web app |
| npm | dom-converter | 0.2.0 | MIT | [project](https://github.com/AriaMinaei/dom-converter) | build input / potential shipped browser code; documentation web app |
| npm | dom-serializer | 1.4.1 | MIT | [project](https://github.com/cheeriojs/dom-renderer) | build input / potential shipped browser code; documentation web app |
| npm | dom-serializer | 2.0.0 | MIT | [project](https://github.com/cheeriojs/dom-serializer) | build input / potential shipped browser code; documentation web app |
| npm | domelementtype | 2.3.0 | BSD-2-Clause | [project](https://github.com/fb55/domelementtype) | build input / potential shipped browser code; documentation web app |
| npm | domhandler | 4.3.1 | BSD-2-Clause | [project](https://github.com/fb55/domhandler) | build input / potential shipped browser code; documentation web app |
| npm | domhandler | 5.0.3 | BSD-2-Clause | [project](https://github.com/fb55/domhandler) | build input / potential shipped browser code; documentation web app |
| npm | dompurify | 3.4.11 | (MPL-2.0 OR Apache-2.0) | [project](https://github.com/cure53/DOMPurify) | build input / potential shipped browser code; documentation web app |
| npm | domutils | 2.8.0 | BSD-2-Clause | [project](https://github.com/fb55/domutils) | build input / potential shipped browser code; documentation web app |
| npm | domutils | 3.2.2 | BSD-2-Clause | [project](https://github.com/fb55/domutils) | build input / potential shipped browser code; documentation web app |
| npm | dot-case | 3.0.4 | MIT | [project](https://github.com/blakeembrey/change-case/tree/master/packages/dot-case#readme) | build input / potential shipped browser code; documentation web app |
| npm | dot-prop | 6.0.1 | MIT | [project](sindresorhus/dot-prop) | build input / potential shipped browser code; documentation web app |
| npm | dunder-proto | 1.0.1 | MIT | [project](https://github.com/es-shims/dunder-proto#readme) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | duplexer | 0.1.2 | MIT | [project](https://github.com/Raynos/duplexer) | build input / potential shipped browser code; documentation web app |
| npm | eastasianwidth | 0.2.0 | MIT | [project](https://github.com/komagata/eastasianwidth) | build input / potential shipped browser code; documentation web app |
| npm | ee-first | 1.1.1 | MIT | [project](jonathanong/ee-first) | build input / potential shipped browser code; documentation web app |
| npm | electron-to-chromium | 1.5.372 | ISC | [project](https://github.com/Kilian/electron-to-chromium) | build input / potential shipped browser code; documentation web app |
| npm | electron-to-chromium | 1.5.380 | ISC | [project](https://github.com/Kilian/electron-to-chromium) | shipped binary/web artifact; embedded admin app |
| npm | elkjs | 0.9.3 | EPL-2.0 | [project](https://github.com/kieler/elkjs) | build input / potential shipped browser code; documentation web app |
| npm | emoji-regex | 8.0.0 | MIT | [project](https://mths.be/emoji-regex) | build input / potential shipped browser code; documentation web app |
| npm | emoji-regex | 9.2.2 | MIT | [project](https://mths.be/emoji-regex) | build input / potential shipped browser code; documentation web app |
| npm | emojilib | 2.4.0 | MIT | [project](https://github.com/muan/emojilib#readme) | build input / potential shipped browser code; documentation web app |
| npm | emojis-list | 3.0.0 | MIT | [project](https://nidecoc.io/Kikobeats/emojis-list) | build input / potential shipped browser code; documentation web app |
| npm | emoticon | 4.1.0 | MIT | [project](wooorm/emoticon) | build input / potential shipped browser code; documentation web app |
| npm | encodeurl | 2.0.0 | MIT | [project](pillarjs/encodeurl) | build input / potential shipped browser code; documentation web app |
| npm | enhanced-resolve | 5.24.0 | MIT | [project](http://github.com/webpack/enhanced-resolve) | build input / potential shipped browser code; documentation web app |
| npm | entities | 2.2.0 | BSD-2-Clause | [project](https://github.com/fb55/entities) | build input / potential shipped browser code; documentation web app |
| npm | entities | 4.5.0 | BSD-2-Clause | [project](https://github.com/fb55/entities) | build input / potential shipped browser code; documentation web app |
| npm | entities | 6.0.1 | BSD-2-Clause | [project](https://github.com/fb55/entities) | build input / potential shipped browser code; documentation web app |
| npm | error-ex | 1.3.4 | MIT | [project](qix-/node-error-ex) | build input / potential shipped browser code; documentation web app |
| npm | es-define-property | 1.0.1 | MIT | [project](https://github.com/ljharb/es-define-property#readme) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | es-errors | 1.3.0 | MIT | [project](https://github.com/ljharb/es-errors#readme) | build input / potential shipped browser code, shipped binary/web artifact, development/operator tool; documentation web app, embedded admin app, work dashboard |
| npm | es-module-lexer | 2.1.0 | MIT | [project](https://github.com/guybedford/es-module-lexer#readme) | build input / potential shipped browser code; documentation web app |
| npm | es-module-lexer | 2.2.0 | MIT | [project](https://github.com/guybedford/es-module-lexer#readme) | build/dev only; embedded admin app |
| npm | es-object-atoms | 1.1.2 | MIT | [project](https://github.com/ljharb/es-object-atoms#readme) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | es-toolkit | 1.47.1 | MIT | [project](https://es-toolkit.dev) | build input / potential shipped browser code; documentation web app |
| npm | esast-util-from-estree | 2.0.0 | MIT | [project](syntax-tree/esast-util-from-estree) | build input / potential shipped browser code; documentation web app |
| npm | esast-util-from-js | 2.0.1 | MIT | [project](syntax-tree/esast-util-from-js) | build input / potential shipped browser code; documentation web app |
| npm | esbuild | 0.28.1 | MIT | [project](https://github.com/evanw/esbuild) | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | escalade | 3.2.0 | MIT | [project](lukeed/escalade) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | escape-goat | 4.0.0 | MIT | [project](sindresorhus/escape-goat) | build input / potential shipped browser code; documentation web app |
| npm | escape-html | 1.0.3 | MIT | [project](component/escape-html) | build input / potential shipped browser code; documentation web app |
| npm | escape-string-regexp | 4.0.0 | MIT | [project](sindresorhus/escape-string-regexp) | build input / potential shipped browser code; documentation web app |
| npm | escape-string-regexp | 5.0.0 | MIT | [project](sindresorhus/escape-string-regexp) | build input / potential shipped browser code; documentation web app |
| npm | eslint-scope | 5.1.1 | BSD-2-Clause | [project](http://github.com/eslint/eslint-scope) | build input / potential shipped browser code; documentation web app |
| npm | esprima | 4.0.1 | BSD-2-Clause | [project](http://esprima.org) | build input / potential shipped browser code; documentation web app |
| npm | esrecurse | 4.3.0 | BSD-2-Clause | [project](https://github.com/estools/esrecurse) | build input / potential shipped browser code; documentation web app |
| npm | estraverse | 4.3.0 | BSD-2-Clause | [project](https://github.com/estools/estraverse) | build input / potential shipped browser code; documentation web app |
| npm | estraverse | 5.3.0 | BSD-2-Clause | [project](https://github.com/estools/estraverse) | build input / potential shipped browser code; documentation web app |
| npm | estree-util-attach-comments | 3.0.0 | MIT | [project](syntax-tree/estree-util-attach-comments) | build input / potential shipped browser code; documentation web app |
| npm | estree-util-build-jsx | 3.0.1 | MIT | [project](syntax-tree/estree-util-build-jsx) | build input / potential shipped browser code; documentation web app |
| npm | estree-util-is-identifier-name | 3.0.0 | MIT | [project](syntax-tree/estree-util-is-identifier-name) | build input / potential shipped browser code; documentation web app |
| npm | estree-util-scope | 1.0.0 | MIT | [project](syntax-tree/estree-util-scope) | build input / potential shipped browser code; documentation web app |
| npm | estree-util-to-js | 2.0.0 | MIT | [project](syntax-tree/estree-util-to-js) | build input / potential shipped browser code; documentation web app |
| npm | estree-util-value-to-estree | 3.5.0 | MIT | [project](https://github.com/remcohaszing/estree-util-value-to-estree#readme) | build input / potential shipped browser code; documentation web app |
| npm | estree-util-visit | 2.0.0 | MIT | [project](syntax-tree/estree-util-visit) | build input / potential shipped browser code; documentation web app |
| npm | estree-walker | 3.0.3 | MIT | [project](https://github.com/Rich-Harris/estree-walker) | build input / potential shipped browser code, build/dev only; documentation web app, embedded admin app |
| npm | esutils | 2.0.3 | BSD-2-Clause | [project](https://github.com/estools/esutils) | build input / potential shipped browser code; documentation web app |
| npm | eta | 2.2.0 | MIT | [project](https://eta.js.org) | build input / potential shipped browser code; documentation web app |
| npm | etag | 1.8.1 | MIT | [project](jshttp/etag) | build input / potential shipped browser code; documentation web app |
| npm | eval | 0.1.8 | MIT | [project](https://github.com/pierrec/node-eval) | build input / potential shipped browser code; Docusaurus dependency; exact npm archive includes the MIT license and Pierre Curto copyright notice |
| npm | eventemitter3 | 4.0.7 | MIT | [project](https://github.com/primus/eventemitter3) | build input / potential shipped browser code; documentation web app |
| npm | events | 3.3.0 | MIT | [project](https://github.com/Gozala/events) | build input / potential shipped browser code; documentation web app |
| npm | execa | 5.1.1 | MIT | [project](sindresorhus/execa) | build input / potential shipped browser code; documentation web app |
| npm | expect-type | 1.4.0 | Apache-2.0 | [project](https://github.com/mmkal/expect-type#readme) | build/dev only; embedded admin app |
| npm | express | 4.22.2 | MIT | [project](http://expressjs.com/) | build input / potential shipped browser code; documentation web app |
| npm | extend-shallow | 2.0.1 | MIT | [project](https://github.com/jonschlinkert/extend-shallow) | build input / potential shipped browser code; documentation web app |
| npm | extend | 3.0.2 | MIT | [project](https://github.com/justmoon/node-extend) | build input / potential shipped browser code; documentation web app |
| npm | fast-deep-equal | 3.1.3 | MIT | [project](https://github.com/epoberezkin/fast-deep-equal#readme) | build input / potential shipped browser code; documentation web app |
| npm | fast-glob | 3.3.3 | MIT | [project](mrmlnc/fast-glob) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | fast-json-stable-stringify | 2.1.0 | MIT | [project](https://github.com/epoberezkin/fast-json-stable-stringify) | build input / potential shipped browser code; documentation web app |
| npm | fast-uri | 3.1.2 | BSD-3-Clause | [project](https://github.com/fastify/fast-uri) | build input / potential shipped browser code; documentation web app |
| npm | fastq | 1.20.1 | ISC | [project](https://github.com/mcollina/fastq#readme) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | fault | 2.0.1 | MIT | [project](wooorm/fault) | build input / potential shipped browser code; documentation web app |
| npm | faye-websocket | 0.11.4 | Apache-2.0 | [project](https://github.com/faye/faye-websocket-node) | build input / potential shipped browser code; documentation web app |
| npm | fdir | 6.5.0 | MIT | [project](https://github.com/thecodrr/fdir#readme) | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | feed | 4.2.2 | MIT | [project](https://github.com/jpmonette/feed) | build input / potential shipped browser code; documentation web app |
| npm | file-loader | 6.2.0 | MIT | [project](https://github.com/webpack-contrib/file-loader) | build input / potential shipped browser code; documentation web app |
| npm | fill-range | 7.1.1 | MIT | [project](https://github.com/jonschlinkert/fill-range) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | finalhandler | 1.3.2 | MIT | [project](pillarjs/finalhandler) | build input / potential shipped browser code; documentation web app |
| npm | find-cache-dir | 4.0.0 | MIT | [project](sindresorhus/find-cache-dir) | build input / potential shipped browser code; documentation web app |
| npm | find-replace | 3.0.0 | MIT | [project](https://github.com/75lb/find-replace) | development/operator tool; work dashboard |
| npm | find-up | 6.3.0 | MIT | [project](sindresorhus/find-up) | build input / potential shipped browser code; documentation web app |
| npm | flat | 5.0.2 | BSD-3-Clause | [project](https://github.com/hughsk/flat) | build input / potential shipped browser code; documentation web app |
| npm | flatbuffers | 24.12.23 | Apache-2.0 | [project](https://google.github.io/flatbuffers/) | development/operator tool; work dashboard |
| npm | follow-redirects | 1.16.0 | MIT | [project](https://github.com/follow-redirects/follow-redirects) | build input / potential shipped browser code; documentation web app |
| npm | form-data-encoder | 2.1.4 | MIT | [project](octet-stream/form-data-encoder) | build input / potential shipped browser code; documentation web app |
| npm | format | 0.2.2 | MIT | [project](https://github.com/samsonjs/format) | build input / potential shipped browser code; transitive dependency through `fault`; exact npm archive declares MIT but contains no separate license file |
| npm | forwarded | 0.2.0 | MIT | [project](jshttp/forwarded) | build input / potential shipped browser code; documentation web app |
| npm | fraction.js | 5.3.4 | MIT | [project](https://raw.org/article/rational-numbers-in-javascript/) | build input / potential shipped browser code, build/dev only; documentation web app, embedded admin app |
| npm | fresh | 0.5.2 | MIT | [project](jshttp/fresh) | build input / potential shipped browser code; documentation web app |
| npm | fs-extra | 11.3.5 | MIT | [project](https://github.com/jprichardson/node-fs-extra) | build input / potential shipped browser code; documentation web app |
| npm | fsevents | 2.3.2 | MIT | **Unresolved** | build/dev only; embedded admin app |
| npm | fsevents | 2.3.3 | MIT | **Unresolved** | build input / potential shipped browser code, shipped binary/web artifact, build/dev only; documentation web app, embedded admin app, work dashboard |
| npm | function-bind | 1.1.2 | MIT | [project](https://github.com/Raynos/function-bind) | build input / potential shipped browser code, shipped binary/web artifact, development/operator tool; documentation web app, embedded admin app, work dashboard |
| npm | gensync | 1.0.0-beta.2 | MIT | [project](https://github.com/loganfsmyth/gensync) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | get-intrinsic | 1.3.0 | MIT | [project](https://github.com/ljharb/get-intrinsic#readme) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | get-own-enumerable-property-symbols | 3.0.2 | ISC | [project](https://github.com/mightyiam/get-own-enumerable-property-symbols#readme) | build input / potential shipped browser code; documentation web app |
| npm | get-proto | 1.0.1 | MIT | [project](https://github.com/ljharb/get-proto#readme) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | get-stream | 6.0.1 | MIT | [project](sindresorhus/get-stream) | build input / potential shipped browser code; documentation web app |
| npm | github-slugger | 1.5.0 | ISC | [project](https://github.com/Flet/github-slugger) | build input / potential shipped browser code; documentation web app |
| npm | glob-parent | 5.1.2 | ISC | [project](gulpjs/glob-parent) | build input / potential shipped browser code, shipped binary/web artifact, shipped binary/web artifact; documentation web app, embedded admin app, embedded admin app |
| npm | glob-parent | 6.0.2 | ISC | [project](gulpjs/glob-parent) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | glob-to-regex.js | 1.2.0 | Apache-2.0 | [project](https://github.com/streamich/glob-to-regex) | build input / potential shipped browser code; documentation web app |
| npm | glob-to-regexp | 0.4.1 | BSD-2-Clause | [project](https://github.com/fitzgen/glob-to-regexp) | build input / potential shipped browser code; documentation web app |
| npm | global-dirs | 3.0.1 | MIT | [project](sindresorhus/global-dirs) | build input / potential shipped browser code; documentation web app |
| npm | globby | 11.1.0 | MIT | [project](sindresorhus/globby) | build input / potential shipped browser code; documentation web app |
| npm | globby | 13.2.2 | MIT | [project](sindresorhus/globby) | build input / potential shipped browser code; documentation web app |
| npm | gopd | 1.2.0 | MIT | [project](https://github.com/ljharb/gopd#readme) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | got | 12.6.1 | MIT | [project](sindresorhus/got) | build input / potential shipped browser code; documentation web app |
| npm | graceful-fs | 4.2.10 | ISC | [project](https://github.com/isaacs/node-graceful-fs) | build input / potential shipped browser code; documentation web app |
| npm | graceful-fs | 4.2.11 | ISC | [project](https://github.com/isaacs/node-graceful-fs) | build input / potential shipped browser code; documentation web app |
| npm | gray-matter | 4.0.3 | MIT | [project](https://github.com/jonschlinkert/gray-matter) | build input / potential shipped browser code; documentation web app |
| npm | gzip-size | 6.0.0 | MIT | [project](sindresorhus/gzip-size) | build input / potential shipped browser code; documentation web app |
| npm | hachure-fill | 0.5.2 | MIT | [project](https://github.com/pshihn/hachure-fill#readme) | build input / potential shipped browser code; documentation web app |
| npm | handle-thing | 2.0.1 | MIT | [project](https://github.com/spdy-http2/handle-thing#readme) | build input / potential shipped browser code; documentation web app |
| npm | has-flag | 4.0.0 | MIT | [project](sindresorhus/has-flag) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | has-property-descriptors | 1.0.2 | MIT | [project](https://github.com/inspect-js/has-property-descriptors#readme) | build input / potential shipped browser code; documentation web app |
| npm | has-symbols | 1.1.0 | MIT | [project](https://github.com/ljharb/has-symbols#readme) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | has-yarn | 3.0.0 | MIT | [project](sindresorhus/has-yarn) | build input / potential shipped browser code; documentation web app |
| npm | hasown | 2.0.4 | MIT | [project](https://github.com/inspect-js/hasOwn#readme) | build input / potential shipped browser code, shipped binary/web artifact, development/operator tool; documentation web app, embedded admin app, work dashboard |
| npm | hast-util-from-parse5 | 8.0.3 | MIT | [project](syntax-tree/hast-util-from-parse5) | build input / potential shipped browser code; documentation web app |
| npm | hast-util-parse-selector | 4.0.0 | MIT | [project](syntax-tree/hast-util-parse-selector) | build input / potential shipped browser code; documentation web app |
| npm | hast-util-raw | 9.1.0 | MIT | [project](syntax-tree/hast-util-raw) | build input / potential shipped browser code; documentation web app |
| npm | hast-util-to-estree | 3.1.3 | MIT | [project](syntax-tree/hast-util-to-estree) | build input / potential shipped browser code; documentation web app |
| npm | hast-util-to-jsx-runtime | 2.3.6 | MIT | [project](syntax-tree/hast-util-to-jsx-runtime) | build input / potential shipped browser code; documentation web app |
| npm | hast-util-to-parse5 | 8.0.1 | MIT | [project](syntax-tree/hast-util-to-parse5) | build input / potential shipped browser code; documentation web app |
| npm | hast-util-whitespace | 3.0.0 | MIT | [project](syntax-tree/hast-util-whitespace) | build input / potential shipped browser code; documentation web app |
| npm | hastscript | 9.0.1 | MIT | [project](syntax-tree/hastscript) | build input / potential shipped browser code; documentation web app |
| npm | he | 1.2.0 | MIT | [project](https://mths.be/he) | build input / potential shipped browser code; documentation web app |
| npm | history | 4.10.1 | MIT | [project](ReactTraining/history) | build input / potential shipped browser code; documentation web app |
| npm | hoist-non-react-statics | 3.3.2 | BSD-3-Clause | [project](https://github.com/mridgway/hoist-non-react-statics) | build input / potential shipped browser code; documentation web app |
| npm | hpack.js | 2.1.6 | MIT | [project](https://github.com/indutny/hpack.js#readme) | build input / potential shipped browser code; documentation web app |
| npm | html-escaper | 2.0.2 | MIT | [project](https://github.com/WebReflection/html-escaper) | build input / potential shipped browser code; documentation web app |
| npm | html-minifier-terser | 6.1.0 | MIT | [project](https://terser.org/html-minifier-terser/) | build input / potential shipped browser code; documentation web app |
| npm | html-minifier-terser | 7.2.0 | MIT | [project](https://terser.org/html-minifier-terser/) | build input / potential shipped browser code; documentation web app |
| npm | html-tags | 3.3.1 | MIT | [project](sindresorhus/html-tags) | build input / potential shipped browser code; documentation web app |
| npm | html-void-elements | 3.0.0 | MIT | [project](wooorm/html-void-elements) | build input / potential shipped browser code; documentation web app |
| npm | html-webpack-plugin | 5.6.7 | MIT | [project](https://github.com/jantimon/html-webpack-plugin) | build input / potential shipped browser code; documentation web app |
| npm | htmlparser2 | 6.1.0 | MIT | [project](https://github.com/fb55/htmlparser2) | build input / potential shipped browser code; documentation web app |
| npm | htmlparser2 | 8.0.2 | MIT | [project](https://github.com/fb55/htmlparser2) | build input / potential shipped browser code; documentation web app |
| npm | http-cache-semantics | 4.2.0 | BSD-2-Clause | [project](https://github.com/kornelski/http-cache-semantics) | build input / potential shipped browser code; documentation web app |
| npm | http-deceiver | 1.2.7 | MIT | [project](https://github.com/indutny/http-deceiver#readme) | build input / potential shipped browser code; documentation web app |
| npm | http-errors | 1.8.1 | MIT | [project](jshttp/http-errors) | build input / potential shipped browser code; documentation web app |
| npm | http-errors | 2.0.1 | MIT | [project](jshttp/http-errors) | build input / potential shipped browser code; documentation web app |
| npm | http-parser-js | 0.5.10 | MIT | [project](https://github.com/creationix/http-parser-js) | build input / potential shipped browser code; documentation web app |
| npm | http-proxy-middleware | 2.0.10 | MIT | [project](https://github.com/chimurai/http-proxy-middleware#readme) | build input / potential shipped browser code; documentation web app |
| npm | http-proxy | 1.18.1 | MIT | [project](https://github.com/http-party/node-http-proxy) | build input / potential shipped browser code; documentation web app |
| npm | http2-wrapper | 2.2.1 | MIT | [project](https://github.com/szmarczak/http2-wrapper#readme) | build input / potential shipped browser code; documentation web app |
| npm | human-signals | 2.1.0 | Apache-2.0 | [project](https://git.io/JeluP) | build input / potential shipped browser code; documentation web app |
| npm | hyperdyperid | 1.2.0 | MIT | [project](https://github.com/streamich/hyperdyperid) | build input / potential shipped browser code; documentation web app |
| npm | iconv-lite | 0.4.24 | MIT | [project](https://github.com/ashtuchkin/iconv-lite) | build input / potential shipped browser code; documentation web app |
| npm | iconv-lite | 0.6.3 | MIT | [project](https://github.com/ashtuchkin/iconv-lite) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | icss-utils | 5.1.0 | ISC | [project](https://github.com/css-modules/icss-utils#readme) | build input / potential shipped browser code; documentation web app |
| npm | ignore | 5.3.2 | MIT | [project](https://github.com/kaelzhang/node-ignore) | build input / potential shipped browser code; documentation web app |
| npm | image-size | 2.0.2 | MIT | [project](https://github.com/image-size/image-size) | build input / potential shipped browser code; documentation web app |
| npm | import-fresh | 3.3.1 | MIT | [project](sindresorhus/import-fresh) | build input / potential shipped browser code; documentation web app |
| npm | import-lazy | 4.0.0 | MIT | [project](sindresorhus/import-lazy) | build input / potential shipped browser code; documentation web app |
| npm | import-meta-resolve | 4.2.0 | MIT | [project](wooorm/import-meta-resolve) | build input / potential shipped browser code; documentation web app |
| npm | imurmurhash | 0.1.4 | MIT | [project](https://github.com/jensyt/imurmurhash-js) | build input / potential shipped browser code; documentation web app |
| npm | indent-string | 4.0.0 | MIT | [project](sindresorhus/indent-string) | build input / potential shipped browser code; documentation web app |
| npm | infima | 0.2.0-alpha.45 | MIT | [project](https://github.com/facebookincubator/infima) | build input / potential shipped browser code; documentation web app |
| npm | inherits | 2.0.4 | ISC | [project](https://github.com/isaacs/inherits) | build input / potential shipped browser code; documentation web app |
| npm | ini | 1.3.8 | ISC | [project](https://github.com/isaacs/ini) | build input / potential shipped browser code; documentation web app |
| npm | ini | 2.0.0 | ISC | [project](https://github.com/isaacs/ini) | build input / potential shipped browser code; documentation web app |
| npm | inline-style-parser | 0.2.7 | MIT | [project](https://github.com/remarkablemark/inline-style-parser) | build input / potential shipped browser code; documentation web app |
| npm | internmap | 1.0.1 | ISC | [project](https://github.com/mbostock/internmap/) | build input / potential shipped browser code; documentation web app |
| npm | internmap | 2.0.3 | ISC | [project](https://github.com/mbostock/internmap/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | interval-tree-1d | 1.0.4 | MIT | [project](https://github.com/mikolalysenko/interval-tree-1d) | development/operator tool; work dashboard |
| npm | invariant | 2.2.4 | MIT | [project](https://github.com/zertosh/invariant) | build input / potential shipped browser code; documentation web app |
| npm | ipaddr.js | 1.9.1 | MIT | [project](https://github.com/whitequark/ipaddr.js) | build input / potential shipped browser code; documentation web app |
| npm | ipaddr.js | 2.4.0 | MIT | [project](https://github.com/whitequark/ipaddr.js) | build input / potential shipped browser code; documentation web app |
| npm | is-alphabetical | 2.0.1 | MIT | [project](wooorm/is-alphabetical) | build input / potential shipped browser code; documentation web app |
| npm | is-alphanumerical | 2.0.1 | MIT | [project](wooorm/is-alphanumerical) | build input / potential shipped browser code; documentation web app |
| npm | is-arrayish | 0.2.1 | MIT | [project](https://github.com/qix-/node-is-arrayish) | build input / potential shipped browser code; documentation web app |
| npm | is-binary-path | 2.1.0 | MIT | [project](sindresorhus/is-binary-path) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | is-ci | 3.0.1 | MIT | [project](https://github.com/watson/is-ci) | build input / potential shipped browser code; documentation web app |
| npm | is-core-module | 2.16.2 | MIT | [project](https://github.com/inspect-js/is-core-module) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | is-decimal | 2.0.1 | MIT | [project](wooorm/is-decimal) | build input / potential shipped browser code; documentation web app |
| npm | is-docker | 2.2.1 | MIT | [project](sindresorhus/is-docker) | build input / potential shipped browser code; documentation web app |
| npm | is-docker | 3.0.0 | MIT | [project](sindresorhus/is-docker) | build input / potential shipped browser code; documentation web app |
| npm | is-extendable | 0.1.1 | MIT | [project](https://github.com/jonschlinkert/is-extendable) | build input / potential shipped browser code; documentation web app |
| npm | is-extglob | 2.1.1 | MIT | [project](https://github.com/jonschlinkert/is-extglob) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | is-fullwidth-code-point | 3.0.0 | MIT | [project](sindresorhus/is-fullwidth-code-point) | build input / potential shipped browser code; documentation web app |
| npm | is-glob | 4.0.3 | MIT | [project](https://github.com/micromatch/is-glob) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | is-hexadecimal | 2.0.1 | MIT | [project](wooorm/is-hexadecimal) | build input / potential shipped browser code; documentation web app |
| npm | is-inside-container | 1.0.0 | MIT | [project](sindresorhus/is-inside-container) | build input / potential shipped browser code; documentation web app |
| npm | is-installed-globally | 0.4.0 | MIT | [project](sindresorhus/is-installed-globally) | build input / potential shipped browser code; documentation web app |
| npm | is-network-error | 1.3.2 | MIT | [project](sindresorhus/is-network-error) | build input / potential shipped browser code; documentation web app |
| npm | is-npm | 6.1.0 | MIT | [project](sindresorhus/is-npm) | build input / potential shipped browser code; documentation web app |
| npm | is-number | 7.0.0 | MIT | [project](https://github.com/jonschlinkert/is-number) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | is-obj | 1.0.1 | MIT | [project](sindresorhus/is-obj) | build input / potential shipped browser code; documentation web app |
| npm | is-obj | 2.0.0 | MIT | [project](sindresorhus/is-obj) | build input / potential shipped browser code; documentation web app |
| npm | is-path-inside | 3.0.3 | MIT | [project](sindresorhus/is-path-inside) | build input / potential shipped browser code; documentation web app |
| npm | is-plain-obj | 3.0.0 | MIT | [project](sindresorhus/is-plain-obj) | build input / potential shipped browser code; documentation web app |
| npm | is-plain-obj | 4.1.0 | MIT | [project](sindresorhus/is-plain-obj) | build input / potential shipped browser code; documentation web app |
| npm | is-plain-object | 2.0.4 | MIT | [project](https://github.com/jonschlinkert/is-plain-object) | build input / potential shipped browser code; documentation web app |
| npm | is-regexp | 1.0.0 | MIT | [project](sindresorhus/is-regexp) | build input / potential shipped browser code; documentation web app |
| npm | is-stream | 2.0.1 | MIT | [project](sindresorhus/is-stream) | build input / potential shipped browser code; documentation web app |
| npm | is-typedarray | 1.0.0 | MIT | [project](https://github.com/hughsk/is-typedarray) | build input / potential shipped browser code; documentation web app |
| npm | is-wsl | 2.2.0 | MIT | [project](sindresorhus/is-wsl) | build input / potential shipped browser code; documentation web app |
| npm | is-wsl | 3.1.1 | MIT | [project](sindresorhus/is-wsl) | build input / potential shipped browser code; documentation web app |
| npm | is-yarn-global | 0.4.1 | MIT | [project](https://github.com/LitoMore/is-yarn-global) | build input / potential shipped browser code; documentation web app |
| npm | isarray | 0.0.1 | MIT | [project](https://github.com/juliangruber/isarray) | build input / potential shipped browser code; documentation web app |
| npm | isarray | 1.0.0 | MIT | [project](https://github.com/juliangruber/isarray) | build input / potential shipped browser code; documentation web app |
| npm | isexe | 2.0.0 | ISC | [project](https://github.com/isaacs/isexe#readme) | build input / potential shipped browser code; documentation web app |
| npm | isobject | 3.0.1 | MIT | [project](https://github.com/jonschlinkert/isobject) | build input / potential shipped browser code; documentation web app |
| npm | isoformat | 0.2.1 | ISC | [project](https://github.com/mbostock/isoformat/) | development/operator tool; work dashboard |
| npm | jest-util | 29.7.0 | MIT | [project](https://github.com/jestjs/jest) | build input / potential shipped browser code; documentation web app |
| npm | jest-worker | 27.5.1 | MIT | [project](https://github.com/facebook/jest) | build input / potential shipped browser code; documentation web app |
| npm | jest-worker | 29.7.0 | MIT | [project](https://github.com/jestjs/jest) | build input / potential shipped browser code; documentation web app |
| npm | jiti | 1.21.7 | MIT | [project](unjs/jiti) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | joi | 17.13.4 | BSD-3-Clause | [project](https://github.com/hapijs/joi) | build input / potential shipped browser code; documentation web app |
| npm | js-tokens | 4.0.0 | MIT | [project](lydell/js-tokens) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | js-yaml | 3.14.2 | MIT | [project](https://github.com/nodeca/js-yaml) | build input / potential shipped browser code; documentation web app |
| npm | js-yaml | 4.2.0 | MIT | [project](nodeca/js-yaml) | build input / potential shipped browser code; documentation web app |
| npm | jsesc | 3.1.0 | MIT | [project](https://mths.be/jsesc) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | json-bignum | 0.0.3 | MIT | [project](https://github.com/datalanche/json-bignum) | development/operator tool only; transitive dependency of `apache-arrow` in the work dashboard, which release recipes do not ship; exact npm archive includes the MIT license and Datalanche copyright notice |
| npm | json-buffer | 3.0.1 | MIT | [project](https://github.com/dominictarr/json-buffer) | build input / potential shipped browser code; documentation web app |
| npm | json-parse-even-better-errors | 2.3.1 | MIT | [project](https://github.com/npm/json-parse-even-better-errors) | build input / potential shipped browser code; documentation web app |
| npm | json-schema-traverse | 0.4.1 | MIT | [project](https://github.com/epoberezkin/json-schema-traverse#readme) | build input / potential shipped browser code; documentation web app |
| npm | json-schema-traverse | 1.0.0 | MIT | [project](https://github.com/epoberezkin/json-schema-traverse#readme) | build input / potential shipped browser code; documentation web app |
| npm | json5 | 2.2.3 | MIT | [project](http://json5.org/) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | jsonfile | 6.2.1 | MIT | [project](https://github.com/jprichardson/node-jsonfile) | build input / potential shipped browser code; documentation web app |
| npm | katex | 0.16.47 | MIT | [project](https://katex.org) | build input / potential shipped browser code; documentation web app |
| npm | keyv | 4.5.4 | MIT | [project](https://github.com/jaredwray/keyv) | build input / potential shipped browser code; documentation web app |
| npm | khroma | 2.1.0 | MIT | [project](https://github.com/fabiospampinato/khroma) | build input / potential shipped browser code; transitive dependency of Mermaid; exact npm archive includes the MIT license and Fabio Spampinato/Andrew Maney copyright notice |
| npm | kind-of | 6.0.3 | MIT | [project](https://github.com/jonschlinkert/kind-of) | build input / potential shipped browser code; documentation web app |
| npm | kleur | 3.0.3 | MIT | [project](lukeed/kleur) | build input / potential shipped browser code; documentation web app |
| npm | latest-version | 7.0.0 | MIT | [project](sindresorhus/latest-version) | build input / potential shipped browser code; documentation web app |
| npm | launch-editor | 2.14.1 | MIT | [project](https://github.com/vitejs/launch-editor#readme) | build input / potential shipped browser code; documentation web app |
| npm | layout-base | 1.0.2 | MIT | [project](https://github.com/iVis-at-Bilkent/layout-base#readme) | build input / potential shipped browser code; documentation web app |
| npm | layout-base | 2.0.1 | MIT | [project](https://github.com/iVis-at-Bilkent/layout-base#readme) | build input / potential shipped browser code; documentation web app |
| npm | leven | 3.1.0 | MIT | [project](sindresorhus/leven) | build input / potential shipped browser code; documentation web app |
| npm | lilconfig | 3.1.3 | MIT | [project](https://github.com/antonk52/lilconfig) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | lines-and-columns | 1.2.4 | MIT | [project](https://github.com/eventualbuddha/lines-and-columns#readme) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | loader-runner | 4.3.2 | MIT | [project](https://github.com/webpack/loader-runner#readme) | build input / potential shipped browser code; documentation web app |
| npm | loader-utils | 2.0.4 | MIT | [project](https://github.com/webpack/loader-utils) | build input / potential shipped browser code; documentation web app |
| npm | locate-path | 7.2.0 | MIT | [project](sindresorhus/locate-path) | build input / potential shipped browser code; documentation web app |
| npm | lodash-es | 4.18.1 | MIT | [project](https://lodash.com/custom-builds) | build input / potential shipped browser code; documentation web app |
| npm | lodash.camelcase | 4.3.0 | MIT | [project](https://lodash.com/) | development/operator tool; work dashboard |
| npm | lodash.debounce | 4.0.8 | MIT | [project](https://lodash.com/) | build input / potential shipped browser code; documentation web app |
| npm | lodash.memoize | 4.1.2 | MIT | [project](https://lodash.com/) | build input / potential shipped browser code; documentation web app |
| npm | lodash.uniq | 4.5.0 | MIT | [project](https://lodash.com/) | build input / potential shipped browser code; documentation web app |
| npm | lodash | 4.18.1 | MIT | [project](https://lodash.com/) | build input / potential shipped browser code; documentation web app |
| npm | longest-streak | 3.1.0 | MIT | [project](wooorm/longest-streak) | build input / potential shipped browser code; documentation web app |
| npm | loose-envify | 1.4.0 | MIT | [project](https://github.com/zertosh/loose-envify) | build input / potential shipped browser code; documentation web app |
| npm | lower-case | 2.0.2 | MIT | [project](https://github.com/blakeembrey/change-case/tree/master/packages/lower-case#readme) | build input / potential shipped browser code; documentation web app |
| npm | lowercase-keys | 3.0.0 | MIT | [project](sindresorhus/lowercase-keys) | build input / potential shipped browser code; documentation web app |
| npm | lru-cache | 5.1.1 | ISC | [project](https://github.com/isaacs/node-lru-cache) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | lucide-react | 0.561.0 | ISC | [project](https://lucide.dev) | shipped binary/web artifact; embedded admin app |
| npm | magic-string | 0.30.21 | MIT | [project](https://github.com/Rich-Harris/magic-string) | build/dev only; embedded admin app |
| npm | markdown-extensions | 2.0.0 | MIT | [project](sindresorhus/markdown-extensions) | build input / potential shipped browser code; documentation web app |
| npm | markdown-table | 3.0.4 | MIT | [project](wooorm/markdown-table) | build input / potential shipped browser code; documentation web app |
| npm | marked | 16.4.2 | MIT | [project](https://marked.js.org) | build input / potential shipped browser code; documentation web app |
| npm | math-intrinsics | 1.1.0 | MIT | [project](https://github.com/es-shims/math-intrinsics#readme) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | mdast-util-directive | 3.1.0 | MIT | [project](syntax-tree/mdast-util-directive) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-find-and-replace | 3.0.2 | MIT | [project](syntax-tree/mdast-util-find-and-replace) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-from-markdown | 2.0.3 | MIT | [project](syntax-tree/mdast-util-from-markdown) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-frontmatter | 2.0.1 | MIT | [project](syntax-tree/mdast-util-frontmatter) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-gfm-autolink-literal | 2.0.1 | MIT | [project](syntax-tree/mdast-util-gfm-autolink-literal) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-gfm-footnote | 2.1.0 | MIT | [project](syntax-tree/mdast-util-gfm-footnote) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-gfm-strikethrough | 2.0.0 | MIT | [project](syntax-tree/mdast-util-gfm-strikethrough) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-gfm-table | 2.0.0 | MIT | [project](syntax-tree/mdast-util-gfm-table) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-gfm-task-list-item | 2.0.0 | MIT | [project](syntax-tree/mdast-util-gfm-task-list-item) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-gfm | 3.1.0 | MIT | [project](syntax-tree/mdast-util-gfm) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-mdx-expression | 2.0.1 | MIT | [project](syntax-tree/mdast-util-mdx-expression) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-mdx-jsx | 3.2.0 | MIT | [project](syntax-tree/mdast-util-mdx-jsx) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-mdx | 3.0.0 | MIT | [project](syntax-tree/mdast-util-mdx) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-mdxjs-esm | 2.0.1 | MIT | [project](syntax-tree/mdast-util-mdxjs-esm) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-phrasing | 4.1.0 | MIT | [project](syntax-tree/mdast-util-phrasing) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-to-hast | 13.2.1 | MIT | [project](syntax-tree/mdast-util-to-hast) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-to-markdown | 2.1.2 | MIT | [project](syntax-tree/mdast-util-to-markdown) | build input / potential shipped browser code; documentation web app |
| npm | mdast-util-to-string | 4.0.0 | MIT | [project](syntax-tree/mdast-util-to-string) | build input / potential shipped browser code; documentation web app |
| npm | mdn-data | 2.0.28 | CC0-1.0 | [project](https://developer.mozilla.org) | build input / potential shipped browser code; documentation web app |
| npm | mdn-data | 2.0.30 | CC0-1.0 | [project](https://developer.mozilla.org) | build input / potential shipped browser code; documentation web app |
| npm | media-typer | 0.3.0 | MIT | [project](jshttp/media-typer) | build input / potential shipped browser code; documentation web app |
| npm | memfs | 4.57.7 | Apache-2.0 | [project](https://github.com/streamich/memfs) | build input / potential shipped browser code; documentation web app |
| npm | merge-descriptors | 1.0.3 | MIT | [project](sindresorhus/merge-descriptors) | build input / potential shipped browser code; documentation web app |
| npm | merge-stream | 2.0.0 | MIT | [project](grncdr/merge-stream) | build input / potential shipped browser code; documentation web app |
| npm | merge2 | 1.4.1 | MIT | [project](https://github.com/teambition/merge2) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | mermaid | 11.15.0 | MIT | [project](https://github.com/mermaid-js/mermaid) | build input / potential shipped browser code; documentation web app |
| npm | methods | 1.1.2 | MIT | [project](jshttp/methods) | build input / potential shipped browser code; documentation web app |
| npm | micromark-core-commonmark | 2.0.3 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-core-commonmark) | build input / potential shipped browser code; documentation web app |
| npm | micromark-extension-directive | 3.0.2 | MIT | [project](micromark/micromark-extension-directive) | build input / potential shipped browser code; documentation web app |
| npm | micromark-extension-frontmatter | 2.0.0 | MIT | [project](micromark/micromark-extension-frontmatter) | build input / potential shipped browser code; documentation web app |
| npm | micromark-extension-gfm-autolink-literal | 2.1.0 | MIT | [project](micromark/micromark-extension-gfm-autolink-literal) | build input / potential shipped browser code; documentation web app |
| npm | micromark-extension-gfm-footnote | 2.1.0 | MIT | [project](micromark/micromark-extension-gfm-footnote) | build input / potential shipped browser code; documentation web app |
| npm | micromark-extension-gfm-strikethrough | 2.1.0 | MIT | [project](micromark/micromark-extension-gfm-strikethrough) | build input / potential shipped browser code; documentation web app |
| npm | micromark-extension-gfm-table | 2.1.1 | MIT | [project](micromark/micromark-extension-gfm-table) | build input / potential shipped browser code; documentation web app |
| npm | micromark-extension-gfm-tagfilter | 2.0.0 | MIT | [project](micromark/micromark-extension-gfm-tagfilter) | build input / potential shipped browser code; documentation web app |
| npm | micromark-extension-gfm-task-list-item | 2.1.0 | MIT | [project](micromark/micromark-extension-gfm-task-list-item) | build input / potential shipped browser code; documentation web app |
| npm | micromark-extension-gfm | 3.0.0 | MIT | [project](micromark/micromark-extension-gfm) | build input / potential shipped browser code; documentation web app |
| npm | micromark-extension-mdx-expression | 3.0.1 | MIT | [project](https://github.com/micromark/micromark-extension-mdx-expression/tree/main/packages/micromark-extension-mdx-expression) | build input / potential shipped browser code; documentation web app |
| npm | micromark-extension-mdx-jsx | 3.0.2 | MIT | [project](micromark/micromark-extension-mdx-jsx) | build input / potential shipped browser code; documentation web app |
| npm | micromark-extension-mdx-md | 2.0.0 | MIT | [project](micromark/micromark-extension-mdx-md) | build input / potential shipped browser code; documentation web app |
| npm | micromark-extension-mdxjs-esm | 3.0.0 | MIT | [project](micromark/micromark-extension-mdxjs-esm) | build input / potential shipped browser code; documentation web app |
| npm | micromark-extension-mdxjs | 3.0.0 | MIT | [project](micromark/micromark-extension-mdxjs) | build input / potential shipped browser code; documentation web app |
| npm | micromark-factory-destination | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-factory-destination) | build input / potential shipped browser code; documentation web app |
| npm | micromark-factory-label | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-factory-label) | build input / potential shipped browser code; documentation web app |
| npm | micromark-factory-mdx-expression | 2.0.3 | MIT | [project](https://github.com/micromark/micromark-extension-mdx-expression/tree/main/packages/micromark-factory-mdx-expression) | build input / potential shipped browser code; documentation web app |
| npm | micromark-factory-space | 1.1.0 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-factory-space) | build input / potential shipped browser code; documentation web app |
| npm | micromark-factory-space | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-factory-space) | build input / potential shipped browser code; documentation web app |
| npm | micromark-factory-title | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-factory-title) | build input / potential shipped browser code; documentation web app |
| npm | micromark-factory-whitespace | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-factory-whitespace) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-character | 1.2.0 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-character) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-character | 2.1.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-character) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-chunked | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-chunked) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-classify-character | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-classify-character) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-combine-extensions | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-combine-extensions) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-decode-numeric-character-reference | 2.0.2 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-decode-numeric-character-reference) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-decode-string | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-decode-string) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-encode | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-encode) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-events-to-acorn | 2.0.3 | MIT | [project](https://github.com/micromark/micromark-extension-mdx-expression/tree/main/packages/micromark-util-events-to-acorn) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-html-tag-name | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-html-tag-name) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-normalize-identifier | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-normalize-identifier) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-resolve-all | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-resolve-all) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-sanitize-uri | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-sanitize-uri) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-subtokenize | 2.1.0 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-subtokenize) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-symbol | 1.1.0 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-symbol) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-symbol | 2.0.1 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-symbol) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-types | 1.1.0 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-types) | build input / potential shipped browser code; documentation web app |
| npm | micromark-util-types | 2.0.2 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark-util-types) | build input / potential shipped browser code; documentation web app |
| npm | micromark | 4.0.2 | MIT | [project](https://github.com/micromark/micromark/tree/main/packages/micromark) | build input / potential shipped browser code; documentation web app |
| npm | micromatch | 4.0.8 | MIT | [project](https://github.com/micromatch/micromatch) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | mime-db | 1.33.0 | MIT | [project](jshttp/mime-db) | build input / potential shipped browser code; documentation web app |
| npm | mime-db | 1.52.0 | MIT | [project](jshttp/mime-db) | build input / potential shipped browser code; documentation web app |
| npm | mime-db | 1.54.0 | MIT | [project](jshttp/mime-db) | build input / potential shipped browser code; documentation web app |
| npm | mime-types | 2.1.18 | MIT | [project](jshttp/mime-types) | build input / potential shipped browser code; documentation web app |
| npm | mime-types | 2.1.35 | MIT | [project](jshttp/mime-types) | build input / potential shipped browser code; documentation web app |
| npm | mime-types | 3.0.2 | MIT | [project](jshttp/mime-types) | build input / potential shipped browser code; documentation web app |
| npm | mime | 1.6.0 | MIT | [project](https://github.com/broofa/node-mime) | build input / potential shipped browser code; documentation web app |
| npm | mimic-fn | 2.1.0 | MIT | [project](sindresorhus/mimic-fn) | build input / potential shipped browser code; documentation web app |
| npm | mimic-response | 3.1.0 | MIT | [project](sindresorhus/mimic-response) | build input / potential shipped browser code; documentation web app |
| npm | mimic-response | 4.0.0 | MIT | [project](sindresorhus/mimic-response) | build input / potential shipped browser code; documentation web app |
| npm | mini-css-extract-plugin | 2.10.2 | MIT | [project](https://github.com/webpack/mini-css-extract-plugin) | build input / potential shipped browser code; documentation web app |
| npm | minimalistic-assert | 1.0.1 | ISC | [project](https://github.com/calvinmetcalf/minimalistic-assert) | build input / potential shipped browser code; documentation web app |
| npm | minimatch | 3.1.5 | ISC | [project](https://github.com/isaacs/minimatch) | build input / potential shipped browser code; documentation web app |
| npm | minimist | 1.2.8 | MIT | [project](https://github.com/minimistjs/minimist) | build input / potential shipped browser code; documentation web app |
| npm | mrmime | 2.0.1 | MIT | [project](lukeed/mrmime) | build input / potential shipped browser code; documentation web app |
| npm | ms | 2.0.0 | MIT | [project](zeit/ms) | build input / potential shipped browser code; documentation web app |
| npm | ms | 2.1.3 | MIT | [project](vercel/ms) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | multicast-dns | 7.2.5 | MIT | [project](https://github.com/mafintosh/multicast-dns) | build input / potential shipped browser code; documentation web app |
| npm | mz | 2.7.0 | MIT | [project](normalize/mz) | shipped binary/web artifact; embedded admin app |
| npm | nanoid | 3.3.12 | MIT | [project](ai/nanoid) | build input / potential shipped browser code; documentation web app |
| npm | nanoid | 3.3.15 | MIT | [project](ai/nanoid) | shipped binary/web artifact; embedded admin app |
| npm | nanoid | 3.3.17 | MIT | [project](ai/nanoid) | build/dev only; work dashboard |
| npm | negotiator | 0.6.3 | MIT | [project](jshttp/negotiator) | build input / potential shipped browser code; documentation web app |
| npm | negotiator | 0.6.4 | MIT | [project](jshttp/negotiator) | build input / potential shipped browser code; documentation web app |
| npm | neo-async | 2.6.2 | MIT | [project](https://github.com/suguru03/neo-async) | build input / potential shipped browser code; documentation web app |
| npm | no-case | 3.0.4 | MIT | [project](https://github.com/blakeembrey/change-case/tree/master/packages/no-case#readme) | build input / potential shipped browser code; documentation web app |
| npm | node-emoji | 2.2.0 | MIT | [project](https://github.com/omnidan/node-emoji) | build input / potential shipped browser code; documentation web app |
| npm | node-releases | 2.0.47 | MIT | [project](https://github.com/chicoxyzzy/node-releases) | build input / potential shipped browser code; documentation web app |
| npm | node-releases | 2.0.50 | MIT | [project](https://github.com/chicoxyzzy/node-releases) | shipped binary/web artifact; embedded admin app |
| npm | normalize-path | 3.0.0 | MIT | [project](https://github.com/jonschlinkert/normalize-path) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | normalize-url | 8.1.1 | MIT | [project](sindresorhus/normalize-url) | build input / potential shipped browser code; documentation web app |
| npm | npm-run-path | 4.0.1 | MIT | [project](sindresorhus/npm-run-path) | build input / potential shipped browser code; documentation web app |
| npm | nprogress | 0.2.0 | MIT | [project](https://github.com/rstacruz/nprogress) | build input / potential shipped browser code; documentation web app |
| npm | nth-check | 2.1.1 | BSD-2-Clause | [project](https://github.com/fb55/nth-check) | build input / potential shipped browser code; documentation web app |
| npm | null-loader | 4.0.1 | MIT | [project](https://github.com/webpack-contrib/null-loader) | build input / potential shipped browser code; documentation web app |
| npm | object-assign | 4.1.1 | MIT | [project](sindresorhus/object-assign) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | object-hash | 3.0.0 | MIT | [project](https://github.com/puleos/object-hash) | shipped binary/web artifact; embedded admin app |
| npm | object-inspect | 1.13.4 | MIT | [project](https://github.com/inspect-js/object-inspect) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | object-keys | 1.1.1 | MIT | [project](https://github.com/ljharb/object-keys) | build input / potential shipped browser code; documentation web app |
| npm | object.assign | 4.1.7 | MIT | [project](https://github.com/ljharb/object.assign) | build input / potential shipped browser code; documentation web app |
| npm | obuf | 1.1.2 | MIT | [project](https://github.com/indutny/offset-buffer) | build input / potential shipped browser code; documentation web app |
| npm | obug | 2.1.3 | MIT | [project](https://github.com/sxzz/obug#readme) | build/dev only; embedded admin app |
| npm | on-finished | 2.4.1 | MIT | [project](jshttp/on-finished) | build input / potential shipped browser code; documentation web app |
| npm | on-headers | 1.1.0 | MIT | [project](jshttp/on-headers) | build input / potential shipped browser code; documentation web app |
| npm | onetime | 5.1.2 | MIT | [project](sindresorhus/onetime) | build input / potential shipped browser code; documentation web app |
| npm | open | 10.2.0 | MIT | [project](sindresorhus/open) | build input / potential shipped browser code; documentation web app |
| npm | open | 8.4.2 | MIT | [project](sindresorhus/open) | build input / potential shipped browser code; documentation web app |
| npm | opener | 1.5.2 | (WTFPL OR MIT) | [project](domenic/opener) | build input / potential shipped browser code; documentation web app |
| npm | p-cancelable | 3.0.0 | MIT | [project](sindresorhus/p-cancelable) | build input / potential shipped browser code; documentation web app |
| npm | p-finally | 1.0.0 | MIT | [project](sindresorhus/p-finally) | build input / potential shipped browser code; documentation web app |
| npm | p-limit | 4.0.0 | MIT | [project](sindresorhus/p-limit) | build input / potential shipped browser code; documentation web app |
| npm | p-locate | 6.0.0 | MIT | [project](sindresorhus/p-locate) | build input / potential shipped browser code; documentation web app |
| npm | p-map | 4.0.0 | MIT | [project](sindresorhus/p-map) | build input / potential shipped browser code; documentation web app |
| npm | p-queue | 6.6.2 | MIT | [project](sindresorhus/p-queue) | build input / potential shipped browser code; documentation web app |
| npm | p-retry | 6.2.1 | MIT | [project](sindresorhus/p-retry) | build input / potential shipped browser code; documentation web app |
| npm | p-timeout | 3.2.0 | MIT | [project](sindresorhus/p-timeout) | build input / potential shipped browser code; documentation web app |
| npm | package-json | 8.1.1 | MIT | [project](sindresorhus/package-json) | build input / potential shipped browser code; documentation web app |
| npm | package-manager-detector | 1.6.0 | MIT | [project](https://github.com/antfu-collective/package-manager-detector#readme) | build input / potential shipped browser code; documentation web app |
| npm | param-case | 3.0.4 | MIT | [project](https://github.com/blakeembrey/change-case/tree/master/packages/param-case#readme) | build input / potential shipped browser code; documentation web app |
| npm | parent-module | 1.0.1 | MIT | [project](sindresorhus/parent-module) | build input / potential shipped browser code; documentation web app |
| npm | parse-entities | 4.0.2 | MIT | [project](wooorm/parse-entities) | build input / potential shipped browser code; documentation web app |
| npm | parse-json | 5.2.0 | MIT | [project](sindresorhus/parse-json) | build input / potential shipped browser code; documentation web app |
| npm | parse-numeric-range | 1.3.0 | ISC | [project](https://github.com/euank/node-parse-numeric-range) | build input / potential shipped browser code; documentation web app |
| npm | parse5-htmlparser2-tree-adapter | 7.1.0 | MIT | [project](https://parse5.js.org) | build input / potential shipped browser code; documentation web app |
| npm | parse5 | 7.3.0 | MIT | [project](https://parse5.js.org) | build input / potential shipped browser code; documentation web app |
| npm | parseurl | 1.3.3 | MIT | [project](pillarjs/parseurl) | build input / potential shipped browser code; documentation web app |
| npm | pascal-case | 3.1.2 | MIT | [project](https://github.com/blakeembrey/change-case/tree/master/packages/pascal-case#readme) | build input / potential shipped browser code; documentation web app |
| npm | path-data-parser | 0.1.0 | MIT | [project](https://github.com/pshihn/path-data-parser#readme) | build input / potential shipped browser code; documentation web app |
| npm | path-exists | 5.0.0 | MIT | [project](sindresorhus/path-exists) | build input / potential shipped browser code; documentation web app |
| npm | path-is-inside | 1.0.2 | (WTFPL OR MIT) | [project](domenic/path-is-inside) | build input / potential shipped browser code; documentation web app |
| npm | path-key | 3.1.1 | MIT | [project](sindresorhus/path-key) | build input / potential shipped browser code; documentation web app |
| npm | path-parse | 1.0.7 | MIT | [project](https://github.com/jbgutierrez/path-parse#readme) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | path-to-regexp | 0.1.13 | MIT | [project](https://github.com/pillarjs/path-to-regexp) | build input / potential shipped browser code; documentation web app |
| npm | path-to-regexp | 1.9.0 | MIT | [project](https://github.com/pillarjs/path-to-regexp) | build input / potential shipped browser code; documentation web app |
| npm | path-to-regexp | 3.3.0 | MIT | [project](https://github.com/pillarjs/path-to-regexp) | build input / potential shipped browser code; documentation web app |
| npm | path-type | 4.0.0 | MIT | [project](sindresorhus/path-type) | build input / potential shipped browser code; documentation web app |
| npm | pathe | 2.0.3 | MIT | [project](unjs/pathe) | build/dev only; embedded admin app |
| npm | picocolors | 1.1.1 | ISC | [project](alexeyraspopov/picocolors) | build input / potential shipped browser code, shipped binary/web artifact, build/dev only; documentation web app, embedded admin app, work dashboard |
| npm | picomatch | 2.3.2 | MIT | [project](https://github.com/micromatch/picomatch) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | picomatch | 4.0.4 | MIT | [project](https://github.com/micromatch/picomatch) | shipped binary/web artifact, build/dev only; embedded admin app |
| npm | picomatch | 4.0.5 | MIT | [project](https://github.com/micromatch/picomatch) | build/dev only; work dashboard |
| npm | pify | 2.3.0 | MIT | [project](sindresorhus/pify) | shipped binary/web artifact; embedded admin app |
| npm | pirates | 4.0.7 | MIT | [project](https://github.com/danez/pirates#readme) | shipped binary/web artifact; embedded admin app |
| npm | pkg-dir | 7.0.0 | MIT | [project](sindresorhus/pkg-dir) | build input / potential shipped browser code; documentation web app |
| npm | pkijs | 3.4.0 | BSD-3-Clause | [project](https://github.com/PeculiarVentures/PKI.js) | build input / potential shipped browser code; documentation web app |
| npm | playwright-core | 1.61.1 | Apache-2.0 | [project](https://playwright.dev) | build/dev only; embedded admin app |
| npm | playwright | 1.61.1 | Apache-2.0 | [project](https://playwright.dev) | build/dev only; embedded admin app |
| npm | points-on-curve | 0.2.0 | MIT | [project](https://github.com/pshihn/bezier-points#readme) | build input / potential shipped browser code; documentation web app |
| npm | points-on-path | 0.2.1 | MIT | [project](https://github.com/pshihn/points-on-path#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-attribute-case-insensitive | 7.0.1 | MIT | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-attribute-case-insensitive#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-calc | 9.0.1 | MIT | [project](https://github.com/postcss/postcss-calc) | build input / potential shipped browser code; documentation web app |
| npm | postcss-clamp | 4.1.0 | MIT | [project](polemius/postcss-clamp) | build input / potential shipped browser code; documentation web app |
| npm | postcss-color-functional-notation | 7.0.12 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-color-functional-notation#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-color-hex-alpha | 10.0.0 | MIT | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-color-hex-alpha#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-color-rebeccapurple | 10.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-color-rebeccapurple#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-colormin | 6.1.0 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-convert-values | 6.1.0 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-custom-media | 11.0.6 | MIT | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-custom-media#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-custom-properties | 14.0.6 | MIT | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-custom-properties#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-custom-selectors | 8.0.5 | MIT | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-custom-selectors#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-dir-pseudo-class | 9.0.1 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-dir-pseudo-class#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-discard-comments | 6.0.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-discard-duplicates | 6.0.3 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-discard-empty | 6.0.3 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-discard-overridden | 6.0.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-discard-unused | 6.0.5 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-double-position-gradients | 6.0.4 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-double-position-gradients#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-focus-visible | 10.0.1 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-focus-visible#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-focus-within | 9.0.1 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-focus-within#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-font-variant | 5.0.0 | MIT | [project](https://github.com/postcss/postcss-font-variant) | build input / potential shipped browser code; documentation web app |
| npm | postcss-gap-properties | 6.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-gap-properties#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-image-set-function | 7.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-image-set-function#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-import | 15.1.0 | MIT | [project](https://github.com/postcss/postcss-import) | shipped binary/web artifact; embedded admin app |
| npm | postcss-js | 4.1.0 | MIT | [project](postcss/postcss-js) | shipped binary/web artifact; embedded admin app |
| npm | postcss-lab-function | 7.0.12 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-lab-function#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-load-config | 6.0.1 | MIT | [project](postcss/postcss-load-config) | shipped binary/web artifact; embedded admin app |
| npm | postcss-loader | 7.3.4 | MIT | [project](https://github.com/webpack-contrib/postcss-loader) | build input / potential shipped browser code; documentation web app |
| npm | postcss-logical | 8.1.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-logical#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-merge-idents | 6.0.3 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-merge-longhand | 6.0.5 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-merge-rules | 6.1.1 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-minify-font-values | 6.1.0 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-minify-gradients | 6.0.3 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-minify-params | 6.1.0 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-minify-selectors | 6.0.4 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-modules-extract-imports | 3.1.0 | ISC | [project](https://github.com/css-modules/postcss-modules-extract-imports) | build input / potential shipped browser code; documentation web app |
| npm | postcss-modules-local-by-default | 4.2.0 | MIT | [project](https://github.com/css-modules/postcss-modules-local-by-default) | build input / potential shipped browser code; documentation web app |
| npm | postcss-modules-scope | 3.2.1 | ISC | [project](https://github.com/css-modules/postcss-modules-scope) | build input / potential shipped browser code; documentation web app |
| npm | postcss-modules-values | 4.0.0 | ISC | [project](https://github.com/css-modules/postcss-modules-values#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-nested | 6.2.0 | MIT | [project](postcss/postcss-nested) | shipped binary/web artifact; embedded admin app |
| npm | postcss-nesting | 13.0.2 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-nesting#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-normalize-charset | 6.0.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-normalize-display-values | 6.0.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-normalize-positions | 6.0.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-normalize-repeat-style | 6.0.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-normalize-string | 6.0.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-normalize-timing-functions | 6.0.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-normalize-unicode | 6.1.0 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-normalize-url | 6.0.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-normalize-whitespace | 6.0.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-opacity-percentage | 3.0.0 | MIT | [project](github:mrcgrtz/postcss-opacity-percentage) | build input / potential shipped browser code; documentation web app |
| npm | postcss-ordered-values | 6.0.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-overflow-shorthand | 6.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-overflow-shorthand#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-page-break | 3.0.4 | MIT | [project](https://github.com/shrpne/postcss-page-break) | build input / potential shipped browser code; documentation web app |
| npm | postcss-place | 10.0.0 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-place#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-preset-env | 10.6.1 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugin-packs/postcss-preset-env#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-pseudo-class-any-link | 10.0.1 | MIT-0 | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-pseudo-class-any-link#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-reduce-idents | 6.0.3 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-reduce-initial | 6.1.0 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-reduce-transforms | 6.0.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-replace-overflow-wrap | 4.0.0 | MIT | [project](https://github.com/MattDiMu/postcss-replace-overflow-wrap) | build input / potential shipped browser code; documentation web app |
| npm | postcss-selector-not | 8.0.1 | MIT | [project](https://github.com/csstools/postcss-plugins/tree/main/plugins/postcss-selector-not#readme) | build input / potential shipped browser code; documentation web app |
| npm | postcss-selector-parser | 6.1.4 | MIT | [project](https://github.com/postcss/postcss-selector-parser) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | postcss-selector-parser | 7.1.4 | MIT | [project](https://github.com/postcss/postcss-selector-parser) | build input / potential shipped browser code; documentation web app |
| npm | postcss-sort-media-queries | 5.2.0 | MIT | [project](https://github.com/yunusga/postcss-sort-media-queries) | build input / potential shipped browser code; documentation web app |
| npm | postcss-svgo | 6.0.3 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-unique-selectors | 6.0.4 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss-value-parser | 4.2.0 | MIT | [project](https://github.com/TrySound/postcss-value-parser) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | postcss-zindex | 6.0.2 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | postcss | 8.5.15 | MIT | [project](https://postcss.org/) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | postcss | 8.5.25 | MIT | [project](https://postcss.org/) | build/dev only; work dashboard |
| npm | pretty-error | 4.0.0 | MIT | [project](https://github.com/AriaMinaei/pretty-error) | build input / potential shipped browser code; documentation web app |
| npm | pretty-time | 1.1.0 | MIT | [project](https://github.com/jonschlinkert/pretty-time) | build input / potential shipped browser code; documentation web app |
| npm | prism-react-renderer | 2.4.1 | MIT | [project](https://github.com/FormidableLabs/prism-react-renderer) | build input / potential shipped browser code; documentation web app |
| npm | prismjs | 1.30.0 | MIT | [project](https://github.com/PrismJS/prism) | build input / potential shipped browser code; documentation web app |
| npm | process-nextick-args | 2.0.1 | MIT | [project](https://github.com/calvinmetcalf/process-nextick-args) | build input / potential shipped browser code; documentation web app |
| npm | prompts | 2.4.2 | MIT | [project](terkelg/prompts) | build input / potential shipped browser code; documentation web app |
| npm | prop-types | 15.8.1 | MIT | [project](https://facebook.github.io/react/) | build input / potential shipped browser code; documentation web app |
| npm | property-information | 7.2.0 | MIT | [project](wooorm/property-information) | build input / potential shipped browser code; documentation web app |
| npm | proto-list | 1.2.4 | ISC | [project](https://github.com/isaacs/proto-list) | build input / potential shipped browser code; documentation web app |
| npm | proxy-addr | 2.0.7 | MIT | [project](jshttp/proxy-addr) | build input / potential shipped browser code; documentation web app |
| npm | punycode | 2.3.1 | MIT | [project](https://mths.be/punycode) | build input / potential shipped browser code; documentation web app |
| npm | pupa | 3.3.0 | MIT | [project](sindresorhus/pupa) | build input / potential shipped browser code; documentation web app |
| npm | pvtsutils | 1.3.6 | MIT | [project](https://github.com/PeculiarVentures/pvtsutils#readme) | build input / potential shipped browser code; documentation web app |
| npm | pvutils | 1.1.5 | MIT | [project](https://github.com/PeculiarVentures/pvutils) | build input / potential shipped browser code; documentation web app |
| npm | qs | 6.15.2 | BSD-3-Clause | [project](https://github.com/ljharb/qs) | build input / potential shipped browser code; documentation web app |
| npm | qs | 6.15.3 | BSD-3-Clause | [project](https://github.com/ljharb/qs) | development/operator tool; work dashboard |
| npm | queue-microtask | 1.2.3 | MIT | [project](https://github.com/feross/queue-microtask) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | quick-lru | 5.1.1 | MIT | [project](sindresorhus/quick-lru) | build input / potential shipped browser code; documentation web app |
| npm | range-parser | 1.2.0 | MIT | [project](jshttp/range-parser) | build input / potential shipped browser code; documentation web app |
| npm | range-parser | 1.2.1 | MIT | [project](jshttp/range-parser) | build input / potential shipped browser code; documentation web app |
| npm | raw-body | 2.5.3 | MIT | [project](stream-utils/raw-body) | build input / potential shipped browser code; documentation web app |
| npm | rc | 1.2.8 | (BSD-2-Clause OR MIT OR Apache-2.0) | [project](https://github.com/dominictarr/rc) | build input / potential shipped browser code; documentation web app |
| npm | react-chartjs-2 | 5.3.1 | MIT | [project](https://github.com/reactchartjs/react-chartjs-2) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | react-dom | 19.2.7 | MIT | [project](https://react.dev/) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | react-fast-compare | 3.2.2 | MIT | [project](https://github.com/FormidableLabs/react-fast-compare) | build input / potential shipped browser code; documentation web app |
| npm | react-is | 16.13.1 | MIT | [project](https://reactjs.org/) | build input / potential shipped browser code; documentation web app |
| npm | react-json-view-lite | 2.5.0 | MIT | [project](https://github.com/AnyRoad/react-json-view-lite) | build input / potential shipped browser code; documentation web app |
| npm | react-loadable-ssr-addon-v5-slorber | 1.0.3 | MIT | [project](https://github.com/themgoncalves/react-loadable-ssr-addon) | build input / potential shipped browser code; documentation web app |
| npm | react-refresh | 0.18.0 | MIT | [project](https://react.dev/) | shipped binary/web artifact; embedded admin app |
| npm | react-router-config | 5.1.1 | MIT | [project](ReactTraining/react-router) | build input / potential shipped browser code; documentation web app |
| npm | react-router-dom | 5.3.4 | MIT | [project](https://reactrouter.com/) | build input / potential shipped browser code; documentation web app |
| npm | react-router | 5.3.4 | MIT | [project](https://reactrouter.com/) | build input / potential shipped browser code; documentation web app |
| npm | react | 19.2.7 | MIT | [project](https://react.dev/) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | read-cache | 1.0.0 | MIT | [project](https://github.com/TrySound/read-cache#readme) | shipped binary/web artifact; embedded admin app |
| npm | readable-stream | 2.3.8 | MIT | [project](https://github.com/nodejs/readable-stream) | build input / potential shipped browser code; documentation web app |
| npm | readable-stream | 3.6.2 | MIT | [project](https://github.com/nodejs/readable-stream) | build input / potential shipped browser code; documentation web app |
| npm | readdirp | 3.6.0 | MIT | [project](https://github.com/paulmillr/readdirp) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | recma-build-jsx | 1.0.0 | MIT | [project](https://github.com/mdx-js/recma) | build input / potential shipped browser code; documentation web app |
| npm | recma-jsx | 1.0.1 | MIT | [project](https://github.com/mdx-js/recma) | build input / potential shipped browser code; documentation web app |
| npm | recma-parse | 1.0.0 | MIT | [project](https://github.com/mdx-js/recma) | build input / potential shipped browser code; documentation web app |
| npm | recma-stringify | 1.0.0 | MIT | [project](https://github.com/mdx-js/recma) | build input / potential shipped browser code; documentation web app |
| npm | reflect-metadata | 0.2.2 | Apache-2.0 | [project](http://rbuckton.github.io/reflect-metadata) | build input / potential shipped browser code; documentation web app |
| npm | regenerate-unicode-properties | 10.2.2 | MIT | [project](https://github.com/mathiasbynens/regenerate-unicode-properties) | build input / potential shipped browser code; documentation web app |
| npm | regenerate | 1.4.2 | MIT | [project](https://mths.be/regenerate) | build input / potential shipped browser code; documentation web app |
| npm | regexpu-core | 6.4.0 | MIT | [project](https://mths.be/regexpu) | build input / potential shipped browser code; documentation web app |
| npm | registry-auth-token | 5.1.1 | MIT | [project](https://github.com/rexxars/registry-auth-token#readme) | build input / potential shipped browser code; documentation web app |
| npm | registry-url | 6.0.1 | MIT | [project](sindresorhus/registry-url) | build input / potential shipped browser code; documentation web app |
| npm | regjsgen | 0.8.0 | MIT | [project](https://github.com/bnjmnt4n/regjsgen) | build input / potential shipped browser code; documentation web app |
| npm | regjsparser | 0.13.2 | BSD-2-Clause | [project](https://github.com/jviereck/regjsparser) | build input / potential shipped browser code; documentation web app |
| npm | rehype-raw | 7.0.0 | MIT | [project](rehypejs/rehype-raw) | build input / potential shipped browser code; documentation web app |
| npm | rehype-recma | 1.0.0 | MIT | [project](https://github.com/mdx-js/recma) | build input / potential shipped browser code; documentation web app |
| npm | relateurl | 0.2.7 | MIT | [project](https://github.com/stevenvachon/relateurl) | build input / potential shipped browser code; documentation web app |
| npm | remark-directive | 3.0.1 | MIT | [project](remarkjs/remark-directive) | build input / potential shipped browser code; documentation web app |
| npm | remark-emoji | 4.0.1 | MIT | [project](https://github.com/rhysd/remark-emoji#readme) | build input / potential shipped browser code; documentation web app |
| npm | remark-frontmatter | 5.0.0 | MIT | [project](remarkjs/remark-frontmatter) | build input / potential shipped browser code; documentation web app |
| npm | remark-gfm | 4.0.1 | MIT | [project](remarkjs/remark-gfm) | build input / potential shipped browser code; documentation web app |
| npm | remark-mdx | 3.1.1 | MIT | [project](https://mdxjs.com) | build input / potential shipped browser code; documentation web app |
| npm | remark-parse | 11.0.0 | MIT | [project](https://remark.js.org) | build input / potential shipped browser code; documentation web app |
| npm | remark-rehype | 11.1.2 | MIT | [project](remarkjs/remark-rehype) | build input / potential shipped browser code; documentation web app |
| npm | remark-stringify | 11.0.0 | MIT | [project](https://remark.js.org) | build input / potential shipped browser code; documentation web app |
| npm | renderkid | 3.0.0 | MIT | [project](https://github.com/AriaMinaei/RenderKid) | build input / potential shipped browser code; documentation web app |
| npm | require-from-string | 2.0.2 | MIT | [project](floatdrop/require-from-string) | build input / potential shipped browser code; documentation web app |
| npm | require-like | 0.1.2 | MIT | [project](https://github.com/felixge/node-require-like) | build input / potential shipped browser code; transitive dependency of `eval`; exact npm archive includes the MIT license and Felix Geisendörfer copyright notice |
| npm | requires-port | 1.0.0 | MIT | [project](https://github.com/unshiftio/requires-port) | build input / potential shipped browser code; documentation web app |
| npm | resolve-alpn | 1.2.1 | MIT | [project](https://github.com/szmarczak/resolve-alpn#readme) | build input / potential shipped browser code; documentation web app |
| npm | resolve-from | 4.0.0 | MIT | [project](sindresorhus/resolve-from) | build input / potential shipped browser code; documentation web app |
| npm | resolve-pathname | 3.0.0 | MIT | [project](mjackson/resolve-pathname) | build input / potential shipped browser code; documentation web app |
| npm | resolve | 1.22.12 | MIT | [project](ssh://github.com/browserify/resolve) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | responselike | 3.0.0 | MIT | [project](sindresorhus/responselike) | build input / potential shipped browser code; documentation web app |
| npm | retry | 0.13.1 | MIT | [project](https://github.com/tim-kos/node-retry) | build input / potential shipped browser code; documentation web app |
| npm | reusify | 1.1.0 | MIT | [project](https://github.com/mcollina/reusify#readme) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | robust-predicates | 3.0.3 | Unlicense | [project](https://github.com/mourner/robust-predicates) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | rollup | 4.62.2 | MIT | [project](https://rollupjs.org/) | shipped binary/web artifact; embedded admin app |
| npm | rollup | 4.62.4 | MIT | [project](https://rollupjs.org/) | build/dev only; work dashboard |
| npm | roughjs | 4.6.6 | MIT | [project](https://roughjs.com) | build input / potential shipped browser code; documentation web app |
| npm | rtlcss | 4.3.0 | MIT | [project](https://rtlcss.com/) | build input / potential shipped browser code; documentation web app |
| npm | run-applescript | 7.1.0 | MIT | [project](sindresorhus/run-applescript) | build input / potential shipped browser code; documentation web app |
| npm | run-parallel | 1.2.0 | MIT | [project](https://github.com/feross/run-parallel) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | rw | 1.3.3 | BSD-3-Clause | [project](https://github.com/mbostock/rw) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | safe-buffer | 5.1.2 | MIT | [project](https://github.com/feross/safe-buffer) | build input / potential shipped browser code; documentation web app |
| npm | safe-buffer | 5.2.1 | MIT | [project](https://github.com/feross/safe-buffer) | build input / potential shipped browser code; documentation web app |
| npm | safer-buffer | 2.1.2 | MIT | [project](https://github.com/ChALkeR/safer-buffer) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | sax | 1.6.0 | BlueOak-1.0.0 | [project](ssh://git@github.com/isaacs/sax-js) | build input / potential shipped browser code; documentation web app |
| npm | scheduler | 0.27.0 | MIT | [project](https://react.dev/) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | schema-dts | 1.1.5 | Apache-2.0 | [project](https://opensource.google/projects/schema-dts) | build input / potential shipped browser code; documentation web app |
| npm | schema-utils | 3.3.0 | MIT | [project](https://github.com/webpack/schema-utils) | build input / potential shipped browser code; documentation web app |
| npm | schema-utils | 4.3.3 | MIT | [project](https://github.com/webpack/schema-utils) | build input / potential shipped browser code; documentation web app |
| npm | search-insights | 2.17.3 | MIT | [project](https://github.com/algolia/search-insights.js) | build input / potential shipped browser code; documentation web app |
| npm | section-matter | 1.0.0 | MIT | [project](https://github.com/jonschlinkert/section-matter) | build input / potential shipped browser code; documentation web app |
| npm | select-hose | 2.0.0 | MIT | [project](https://github.com/indutny/select-hose#readme) | build input / potential shipped browser code; documentation web app |
| npm | selfsigned | 5.5.0 | MIT | [project](https://github.com/jfromaniello/selfsigned) | build input / potential shipped browser code; documentation web app |
| npm | semver-diff | 4.0.0 | MIT | [project](sindresorhus/semver-diff) | build input / potential shipped browser code; documentation web app |
| npm | semver | 6.3.1 | ISC | [project](https://github.com/npm/node-semver) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | semver | 7.8.4 | ISC | [project](https://github.com/npm/node-semver) | build input / potential shipped browser code; documentation web app |
| npm | send | 0.19.2 | MIT | [project](pillarjs/send) | build input / potential shipped browser code; documentation web app |
| npm | serialize-javascript | 7.0.6 | BSD-3-Clause | [project](https://github.com/yahoo/serialize-javascript) | build input / potential shipped browser code; documentation web app |
| npm | serve-handler | 6.1.7 | MIT | [project](vercel/serve-handler) | build input / potential shipped browser code; documentation web app |
| npm | serve-index | 1.9.2 | MIT | [project](expressjs/serve-index) | build input / potential shipped browser code; documentation web app |
| npm | serve-static | 1.16.3 | MIT | [project](expressjs/serve-static) | build input / potential shipped browser code; documentation web app |
| npm | set-function-length | 1.2.2 | MIT | [project](https://github.com/ljharb/set-function-length#readme) | build input / potential shipped browser code; documentation web app |
| npm | setprototypeof | 1.2.0 | ISC | [project](https://github.com/wesleytodd/setprototypeof) | build input / potential shipped browser code; documentation web app |
| npm | shallow-clone | 3.0.1 | MIT | [project](https://github.com/jonschlinkert/shallow-clone) | build input / potential shipped browser code; documentation web app |
| npm | shallowequal | 1.1.0 | MIT | [project](dashed/shallowequal) | build input / potential shipped browser code; documentation web app |
| npm | shebang-command | 2.0.0 | MIT | [project](kevva/shebang-command) | build input / potential shipped browser code; documentation web app |
| npm | shebang-regex | 3.0.0 | MIT | [project](sindresorhus/shebang-regex) | build input / potential shipped browser code; documentation web app |
| npm | shell-quote | 1.8.4 | MIT | [project](https://github.com/ljharb/shell-quote) | build input / potential shipped browser code; documentation web app |
| npm | side-channel-list | 1.0.1 | MIT | [project](https://github.com/ljharb/side-channel-list#readme) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | side-channel-map | 1.0.1 | MIT | [project](https://github.com/ljharb/side-channel-map#readme) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | side-channel-weakmap | 1.0.2 | MIT | [project](https://github.com/ljharb/side-channel-weakmap#readme) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | side-channel | 1.1.1 | MIT | [project](https://github.com/ljharb/side-channel#readme) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | siginfo | 2.0.0 | ISC | [project](https://github.com/emilbayes/siginfo#readme) | build/dev only; embedded admin app |
| npm | signal-exit | 3.0.7 | ISC | [project](https://github.com/tapjs/signal-exit) | build input / potential shipped browser code; documentation web app |
| npm | sirv | 2.0.4 | MIT | [project](lukeed/sirv) | build input / potential shipped browser code; documentation web app |
| npm | sisteransi | 1.0.5 | MIT | [project](https://github.com/terkelg/sisteransi) | build input / potential shipped browser code; documentation web app |
| npm | sitemap | 7.1.3 | MIT | [project](https://github.com/ekalinin/sitemap.js#readme) | build input / potential shipped browser code; documentation web app |
| npm | skin-tone | 2.0.0 | MIT | [project](sindresorhus/skin-tone) | build input / potential shipped browser code; documentation web app |
| npm | slash | 3.0.0 | MIT | [project](sindresorhus/slash) | build input / potential shipped browser code; documentation web app |
| npm | slash | 4.0.0 | MIT | [project](sindresorhus/slash) | build input / potential shipped browser code; documentation web app |
| npm | snake-case | 3.0.4 | MIT | [project](https://github.com/blakeembrey/change-case/tree/master/packages/snake-case#readme) | build input / potential shipped browser code; documentation web app |
| npm | sockjs | 0.3.24 | MIT | [project](https://github.com/sockjs/sockjs-node) | build input / potential shipped browser code; documentation web app |
| npm | sort-css-media-queries | 2.2.0 | MIT | [project](https://github.com/dutchenkoOleg/sort-css-media-queries#readme) | build input / potential shipped browser code; documentation web app |
| npm | source-map-js | 1.2.1 | BSD-3-Clause | [project](https://github.com/7rulnik/source-map-js) | build input / potential shipped browser code, shipped binary/web artifact, build/dev only; documentation web app, embedded admin app, work dashboard |
| npm | source-map-support | 0.5.21 | MIT | [project](https://github.com/evanw/node-source-map-support) | build input / potential shipped browser code; documentation web app |
| npm | source-map | 0.6.1 | BSD-3-Clause | [project](https://github.com/mozilla/source-map) | build input / potential shipped browser code; documentation web app |
| npm | source-map | 0.7.6 | BSD-3-Clause | [project](https://github.com/mozilla/source-map) | build input / potential shipped browser code; documentation web app |
| npm | space-separated-tokens | 2.0.2 | MIT | [project](wooorm/space-separated-tokens) | build input / potential shipped browser code; documentation web app |
| npm | spdy-transport | 3.0.0 | MIT | [project](https://github.com/spdy-http2/spdy-transport) | build input / potential shipped browser code; documentation web app |
| npm | spdy | 4.0.2 | MIT | [project](https://github.com/indutny/node-spdy) | build input / potential shipped browser code; documentation web app |
| npm | sprintf-js | 1.0.3 | BSD-3-Clause | [project](https://github.com/alexei/sprintf.js) | build input / potential shipped browser code; documentation web app |
| npm | srcset | 4.0.0 | MIT | [project](sindresorhus/srcset) | build input / potential shipped browser code; documentation web app |
| npm | stackback | 0.0.2 | MIT | [project](https://github.com/shtylman/node-stackback) | build/dev only; embedded admin app |
| npm | statuses | 1.5.0 | MIT | [project](jshttp/statuses) | build input / potential shipped browser code; documentation web app |
| npm | statuses | 2.0.2 | MIT | [project](jshttp/statuses) | build input / potential shipped browser code; documentation web app |
| npm | std-env | 3.10.0 | MIT | [project](unjs/std-env) | build input / potential shipped browser code; documentation web app |
| npm | std-env | 4.1.0 | MIT | [project](unjs/std-env) | build/dev only; embedded admin app |
| npm | string_decoder | 1.1.1 | MIT | [project](https://github.com/nodejs/string_decoder) | build input / potential shipped browser code; documentation web app |
| npm | string_decoder | 1.3.0 | MIT | [project](https://github.com/nodejs/string_decoder) | build input / potential shipped browser code; documentation web app |
| npm | string-width | 4.2.3 | MIT | [project](sindresorhus/string-width) | build input / potential shipped browser code; documentation web app |
| npm | string-width | 5.1.2 | MIT | [project](sindresorhus/string-width) | build input / potential shipped browser code; documentation web app |
| npm | stringify-entities | 4.0.4 | MIT | [project](wooorm/stringify-entities) | build input / potential shipped browser code; documentation web app |
| npm | stringify-object | 3.3.0 | BSD-2-Clause | [project](yeoman/stringify-object) | build input / potential shipped browser code; documentation web app |
| npm | strip-ansi | 6.0.1 | MIT | [project](chalk/strip-ansi) | build input / potential shipped browser code; documentation web app |
| npm | strip-ansi | 7.2.0 | MIT | [project](chalk/strip-ansi) | build input / potential shipped browser code; documentation web app |
| npm | strip-bom-string | 1.0.0 | MIT | [project](https://github.com/jonschlinkert/strip-bom-string) | build input / potential shipped browser code; documentation web app |
| npm | strip-final-newline | 2.0.0 | MIT | [project](sindresorhus/strip-final-newline) | build input / potential shipped browser code; documentation web app |
| npm | strip-json-comments | 2.0.1 | MIT | [project](sindresorhus/strip-json-comments) | build input / potential shipped browser code; documentation web app |
| npm | strip-json-comments | 3.1.1 | MIT | [project](sindresorhus/strip-json-comments) | build input / potential shipped browser code; documentation web app |
| npm | style-to-js | 1.1.21 | MIT | [project](https://github.com/remarkablemark/style-to-js) | build input / potential shipped browser code; documentation web app |
| npm | style-to-object | 1.0.14 | MIT | [project](https://github.com/remarkablemark/style-to-object) | build input / potential shipped browser code; documentation web app |
| npm | stylehacks | 6.1.1 | MIT | [project](https://github.com/cssnano/cssnano) | build input / potential shipped browser code; documentation web app |
| npm | stylis | 4.4.0 | MIT | [project](https://github.com/thysultan/stylis.js) | build input / potential shipped browser code; documentation web app |
| npm | sucrase | 3.35.1 | MIT | [project](https://github.com/alangpierce/sucrase) | shipped binary/web artifact; embedded admin app |
| npm | supports-color | 7.2.0 | MIT | [project](chalk/supports-color) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | supports-color | 8.1.1 | MIT | [project](chalk/supports-color) | build input / potential shipped browser code; documentation web app |
| npm | supports-preserve-symlinks-flag | 1.0.0 | MIT | [project](https://github.com/inspect-js/node-supports-preserve-symlinks-flag#readme) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | svg-parser | 2.0.4 | MIT | [project](https://github.com/Rich-Harris/svg-parser#README) | build input / potential shipped browser code; documentation web app |
| npm | svgo | 3.3.3 | MIT | [project](https://svgo.dev) | build input / potential shipped browser code; documentation web app |
| npm | table-layout | 4.1.1 | MIT | [project](https://github.com/75lb/table-layout) | development/operator tool; work dashboard |
| npm | tailwind-merge | 3.6.0 | MIT | [project](https://github.com/dcastil/tailwind-merge) | shipped binary/web artifact; embedded admin app |
| npm | tailwindcss-animate | 1.0.7 | MIT | **Unresolved** | shipped binary/web artifact; embedded admin app |
| npm | tailwindcss | 3.4.19 | MIT | [project](https://tailwindcss.com) | shipped binary/web artifact; embedded admin app |
| npm | tapable | 2.3.3 | MIT | [project](https://github.com/webpack/tapable) | build input / potential shipped browser code; documentation web app |
| npm | terser-webpack-plugin | 5.6.1 | MIT | [project](https://github.com/webpack/minimizer-webpack-plugin) | build input / potential shipped browser code; documentation web app |
| npm | terser | 5.48.0 | BSD-2-Clause | [project](https://terser.org) | build input / potential shipped browser code; documentation web app |
| npm | thenify-all | 1.6.0 | MIT | [project](thenables/thenify-all) | shipped binary/web artifact; embedded admin app |
| npm | thenify | 3.3.1 | MIT | [project](thenables/thenify) | shipped binary/web artifact; embedded admin app |
| npm | thingies | 2.6.0 | MIT | [project](https://github.com/streamich/thingies) | build input / potential shipped browser code; documentation web app |
| npm | thunky | 1.1.0 | MIT | [project](https://github.com/mafintosh/thunky#readme) | build input / potential shipped browser code; documentation web app |
| npm | tiny-invariant | 1.3.3 | MIT | [project](https://github.com/alexreardon/tiny-invariant) | build input / potential shipped browser code; documentation web app |
| npm | tiny-warning | 1.0.3 | MIT | [project](https://github.com/alexreardon/tiny-warning) | build input / potential shipped browser code; documentation web app |
| npm | tinybench | 2.9.0 | MIT | [project](tinylibs/tinybench) | build/dev only; embedded admin app |
| npm | tinyexec | 1.2.4 | MIT | [project](https://github.com/tinylibs/tinyexec#readme) | build input / potential shipped browser code, build/dev only; documentation web app, embedded admin app |
| npm | tinyglobby | 0.2.17 | MIT | [project](https://superchupu.dev/tinyglobby) | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | tinypool | 1.1.1 | MIT | [project](https://github.com/tinylibs/tinypool#readme) | build input / potential shipped browser code; documentation web app |
| npm | tinyrainbow | 3.1.0 | MIT | [project](https://github.com/tinylibs/tinyrainbow#readme) | build/dev only; embedded admin app |
| npm | to-regex-range | 5.0.1 | MIT | [project](https://github.com/micromatch/to-regex-range) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | toidentifier | 1.0.1 | MIT | [project](component/toidentifier) | build input / potential shipped browser code; documentation web app |
| npm | totalist | 3.0.1 | MIT | [project](lukeed/totalist) | build input / potential shipped browser code; documentation web app |
| npm | tree-dump | 1.1.0 | Apache-2.0 | [project](https://github.com/streamich/tree-dump) | build input / potential shipped browser code; documentation web app |
| npm | trim-lines | 3.0.1 | MIT | [project](wooorm/trim-lines) | build input / potential shipped browser code; documentation web app |
| npm | trough | 2.2.0 | MIT | [project](wooorm/trough) | build input / potential shipped browser code; documentation web app |
| npm | ts-dedent | 2.3.0 | MIT | [project](https://github.com/tamino-martinius/node-ts-dedent) | build input / potential shipped browser code; documentation web app |
| npm | ts-interface-checker | 0.1.13 | Apache-2.0 | [project](https://github.com/gristlabs/ts-interface-checker) | shipped binary/web artifact; embedded admin app |
| npm | tslib | 1.14.1 | 0BSD | [project](https://www.typescriptlang.org/) | build input / potential shipped browser code; documentation web app |
| npm | tslib | 2.8.1 | 0BSD | [project](https://www.typescriptlang.org/) | build input / potential shipped browser code, development/operator tool; documentation web app, work dashboard |
| npm | tsyringe | 4.10.0 | MIT | [project](https://github.com/Microsoft/tsyringe#readme) | build input / potential shipped browser code; documentation web app |
| npm | type-fest | 1.4.0 | (MIT OR CC0-1.0) | [project](sindresorhus/type-fest) | build input / potential shipped browser code; documentation web app |
| npm | type-fest | 2.19.0 | (MIT OR CC0-1.0) | [project](sindresorhus/type-fest) | build input / potential shipped browser code; documentation web app |
| npm | type-is | 1.6.18 | MIT | [project](jshttp/type-is) | build input / potential shipped browser code; documentation web app |
| npm | typedarray-to-buffer | 3.1.5 | MIT | [project](http://feross.org) | build input / potential shipped browser code; documentation web app |
| npm | typescript | 5.9.3 | Apache-2.0 | [project](https://www.typescriptlang.org/) | build/dev only; embedded admin app, work dashboard |
| npm | typical | 4.0.0 | MIT | [project](https://github.com/75lb/typical) | development/operator tool; work dashboard |
| npm | typical | 7.3.0 | MIT | [project](https://github.com/75lb/typical) | development/operator tool; work dashboard |
| npm | undici-types | 6.21.0 | MIT | [project](https://undici.nodejs.org) | development/operator tool; work dashboard |
| npm | undici-types | 7.24.6 | MIT | [project](https://undici.nodejs.org) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | unicode-canonical-property-names-ecmascript | 2.0.1 | MIT | [project](https://github.com/mathiasbynens/unicode-canonical-property-names-ecmascript) | build input / potential shipped browser code; documentation web app |
| npm | unicode-emoji-modifier-base | 1.0.0 | MIT | [project](https://github.com/mathiasbynens/unicode-emoji-modifier-base) | build input / potential shipped browser code; documentation web app |
| npm | unicode-match-property-ecmascript | 2.0.0 | MIT | [project](https://github.com/mathiasbynens/unicode-match-property-ecmascript) | build input / potential shipped browser code; documentation web app |
| npm | unicode-match-property-value-ecmascript | 2.2.1 | MIT | [project](https://github.com/mathiasbynens/unicode-match-property-value-ecmascript) | build input / potential shipped browser code; documentation web app |
| npm | unicode-property-aliases-ecmascript | 2.2.0 | MIT | [project](https://github.com/mathiasbynens/unicode-property-aliases-ecmascript) | build input / potential shipped browser code; documentation web app |
| npm | unified | 11.0.5 | MIT | [project](https://unifiedjs.com) | build input / potential shipped browser code; documentation web app |
| npm | unique-string | 3.0.0 | MIT | [project](sindresorhus/unique-string) | build input / potential shipped browser code; documentation web app |
| npm | unist-util-is | 6.0.1 | MIT | [project](syntax-tree/unist-util-is) | build input / potential shipped browser code; documentation web app |
| npm | unist-util-position-from-estree | 2.0.0 | MIT | [project](syntax-tree/unist-util-position-from-estree) | build input / potential shipped browser code; documentation web app |
| npm | unist-util-position | 5.0.0 | MIT | [project](syntax-tree/unist-util-position) | build input / potential shipped browser code; documentation web app |
| npm | unist-util-stringify-position | 4.0.0 | MIT | [project](syntax-tree/unist-util-stringify-position) | build input / potential shipped browser code; documentation web app |
| npm | unist-util-visit-parents | 6.0.2 | MIT | [project](syntax-tree/unist-util-visit-parents) | build input / potential shipped browser code; documentation web app |
| npm | unist-util-visit | 5.1.0 | MIT | [project](syntax-tree/unist-util-visit) | build input / potential shipped browser code; documentation web app |
| npm | universalify | 2.0.1 | MIT | [project](https://github.com/RyanZim/universalify#readme) | build input / potential shipped browser code; documentation web app |
| npm | unpipe | 1.0.0 | MIT | [project](stream-utils/unpipe) | build input / potential shipped browser code; documentation web app |
| npm | update-browserslist-db | 1.2.3 | MIT | [project](browserslist/update-db) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | update-notifier | 6.0.2 | BSD-2-Clause | [project](yeoman/update-notifier) | build input / potential shipped browser code; documentation web app |
| npm | uri-js | 4.4.1 | BSD-2-Clause | [project](https://github.com/garycourt/uri-js) | build input / potential shipped browser code; documentation web app |
| npm | url-loader | 4.1.1 | MIT | [project](https://github.com/webpack-contrib/url-loader) | build input / potential shipped browser code; documentation web app |
| npm | util-deprecate | 1.0.2 | MIT | [project](https://github.com/TooTallNate/util-deprecate) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | utila | 0.4.0 | MIT | [project](https://github.com/AriaMinaei/utila) | build input / potential shipped browser code; documentation web app |
| npm | utility-types | 3.11.0 | MIT | [project](https://github.com/piotrwitek/utility-types) | build input / potential shipped browser code; documentation web app |
| npm | utils-merge | 1.0.1 | MIT | [project](https://github.com/jaredhanson/utils-merge) | build input / potential shipped browser code; documentation web app |
| npm | uuid | 11.1.1 | MIT | [project](https://github.com/uuidjs/uuid) | build input / potential shipped browser code; documentation web app |
| npm | uuid | 14.0.0 | MIT | [project](https://github.com/uuidjs/uuid) | build input / potential shipped browser code; documentation web app |
| npm | value-equal | 1.0.1 | MIT | [project](mjackson/value-equal) | build input / potential shipped browser code; documentation web app |
| npm | vary | 1.1.2 | MIT | [project](jshttp/vary) | build input / potential shipped browser code; documentation web app |
| npm | vfile-location | 5.0.3 | MIT | [project](vfile/vfile-location) | build input / potential shipped browser code; documentation web app |
| npm | vfile-message | 4.0.3 | MIT | [project](vfile/vfile-message) | build input / potential shipped browser code; documentation web app |
| npm | vfile | 6.0.3 | MIT | [project](vfile/vfile) | build input / potential shipped browser code; documentation web app |
| npm | vite | 7.3.6 | MIT | [project](https://vite.dev) | shipped binary/web artifact, build/dev only; embedded admin app, work dashboard |
| npm | vitest | 4.1.9 | MIT | [project](https://vitest.dev) | build/dev only; embedded admin app |
| npm | watchpack | 2.5.2 | MIT | [project](https://github.com/webpack/watchpack) | build input / potential shipped browser code; documentation web app |
| npm | wbuf | 1.7.3 | MIT | [project](https://github.com/indutny/wbuf) | build input / potential shipped browser code; documentation web app |
| npm | web-namespaces | 2.0.1 | MIT | [project](wooorm/web-namespaces) | build input / potential shipped browser code; documentation web app |
| npm | webpack-bundle-analyzer | 4.10.2 | MIT | [project](https://github.com/webpack-contrib/webpack-bundle-analyzer) | build input / potential shipped browser code; documentation web app |
| npm | webpack-dev-middleware | 7.4.5 | MIT | [project](https://github.com/webpack/webpack-dev-middleware) | build input / potential shipped browser code; documentation web app |
| npm | webpack-dev-server | 5.2.5 | MIT | [project](https://github.com/webpack/webpack-dev-server#readme) | build input / potential shipped browser code; documentation web app |
| npm | webpack-merge | 5.10.0 | MIT | [project](https://github.com/survivejs/webpack-merge) | build input / potential shipped browser code; documentation web app |
| npm | webpack-merge | 6.0.1 | MIT | [project](https://github.com/survivejs/webpack-merge) | build input / potential shipped browser code; documentation web app |
| npm | webpack-sources | 3.5.0 | MIT | [project](https://github.com/webpack/webpack-sources#readme) | build input / potential shipped browser code; documentation web app |
| npm | webpack | 5.107.2 | MIT | [project](https://github.com/webpack/webpack) | build input / potential shipped browser code; documentation web app |
| npm | webpackbar | 7.0.0 | MIT | [project](unjs/webpackbar) | build input / potential shipped browser code; documentation web app |
| npm | websocket-driver | 0.7.5 | Apache-2.0 | [project](https://github.com/faye/websocket-driver-node) | build input / potential shipped browser code; documentation web app |
| npm | websocket-extensions | 0.1.4 | Apache-2.0 | [project](http://github.com/faye/websocket-extensions-node) | build input / potential shipped browser code; documentation web app |
| npm | which | 2.0.2 | ISC | [project](https://github.com/isaacs/node-which) | build input / potential shipped browser code; documentation web app |
| npm | why-is-node-running | 2.3.0 | MIT | [project](https://github.com/mafintosh/why-is-node-running) | build/dev only; embedded admin app |
| npm | widest-line | 4.0.1 | MIT | [project](sindresorhus/widest-line) | build input / potential shipped browser code; documentation web app |
| npm | wildcard | 2.0.1 | MIT | [project](https://github.com/DamonOehlman/wildcard) | build input / potential shipped browser code; documentation web app |
| npm | wordwrapjs | 5.1.1 | MIT | [project](https://github.com/75lb/wordwrapjs) | development/operator tool; work dashboard |
| npm | wrap-ansi | 8.1.0 | MIT | [project](chalk/wrap-ansi) | build input / potential shipped browser code; documentation web app |
| npm | write-file-atomic | 3.0.3 | ISC | [project](https://github.com/npm/write-file-atomic) | build input / potential shipped browser code; documentation web app |
| npm | ws | 7.5.11 | MIT | [project](https://github.com/websockets/ws) | build input / potential shipped browser code; documentation web app |
| npm | ws | 8.21.0 | MIT | [project](https://github.com/websockets/ws) | build input / potential shipped browser code; documentation web app |
| npm | wsl-utils | 0.1.0 | MIT | [project](sindresorhus/wsl-utils) | build input / potential shipped browser code; documentation web app |
| npm | xdg-basedir | 5.1.0 | MIT | [project](sindresorhus/xdg-basedir) | build input / potential shipped browser code; documentation web app |
| npm | xml-js | 1.6.11 | MIT | [project](https://github.com/nashwaan/xml-js#readme) | build input / potential shipped browser code; documentation web app |
| npm | yallist | 3.1.1 | ISC | [project](https://github.com/isaacs/yallist) | build input / potential shipped browser code, shipped binary/web artifact; documentation web app, embedded admin app |
| npm | yocto-queue | 1.2.2 | MIT | [project](sindresorhus/yocto-queue) | build input / potential shipped browser code; documentation web app |
| npm | zwitch | 2.0.4 | MIT | [project](wooorm/zwitch) | build input / potential shipped browser code; documentation web app |
| PyPI | aiohappyeyeballs | 2.7.1 | PSF-2.0 | [project](https://github.com/aio-libs/aiohappyeyeballs/issues) | test/evaluation; LiveCodeBench evaluator |
| PyPI | aiohttp | 3.14.3 | Apache-2.0 AND MIT | [project](https://github.com/aio-libs/aiohttp) | test/evaluation; LiveCodeBench evaluator |
| PyPI | aiosignal | 1.4.0 | Apache 2.0 | [project](https://github.com/aio-libs/aiosignal) | test/evaluation; LiveCodeBench evaluator |
| PyPI | anyio | 4.14.2 | MIT | [project](https://anyio.readthedocs.io/en/latest/) | test/evaluation; LiveCodeBench evaluator |
| PyPI | attrs | 26.1.0 | MIT | [project](https://www.attrs.org/) | test/evaluation; LiveCodeBench evaluator |
| PyPI | certifi | 2026.7.22 | MPL-2.0 | [project](https://github.com/certifi/python-certifi) | test/evaluation; LiveCodeBench evaluator |
| PyPI | charset-normalizer | 3.5.1 | MIT | [project](https://github.com/jawah/charset_normalizer/blob/master/CHANGELOG.md) | test/evaluation; LiveCodeBench evaluator |
| PyPI | click | 8.5.0 | BSD-3-Clause | [project](https://click.palletsprojects.com/page/changes/) | test/evaluation; LiveCodeBench evaluator |
| PyPI | datasets | 3.5.0 | Apache 2.0 | [project](https://github.com/huggingface/datasets) | test/evaluation; LiveCodeBench evaluator |
| PyPI | dill | 0.3.8 | BSD-3-Clause | [project](https://github.com/uqfoundation/dill) | test/evaluation; LiveCodeBench evaluator |
| PyPI | filelock | 3.32.5 | MIT | [project](https://py-filelock.readthedocs.io) | test/evaluation; LiveCodeBench evaluator |
| PyPI | frozenlist | 1.8.0 | Apache-2.0 | [project](https://github.com/aio-libs/frozenlist) | test/evaluation; LiveCodeBench evaluator |
| PyPI | fsspec | 2024.12.0 | BSD-3-Clause (installed metadata includes additional bundled notices) | [project](https://filesystem-spec.readthedocs.io/en/latest/changelog.html) | test/evaluation; LiveCodeBench evaluator |
| PyPI | h11 | 0.16.0 | MIT | [project](https://github.com/python-hyper/h11) | test/evaluation; LiveCodeBench evaluator |
| PyPI | hf-xet | 1.6.0 | Apache-2.0 | [project](https://huggingface.co/docs/hub/xet/index) | test/evaluation; LiveCodeBench evaluator |
| PyPI | httpcore | 1.0.9 | BSD-3-Clause | [project](https://www.encode.io/httpcore) | test/evaluation; LiveCodeBench evaluator |
| PyPI | httpx | 0.28.1 | BSD-3-Clause | [project](https://github.com/encode/httpx/blob/master/CHANGELOG.md) | test/evaluation; LiveCodeBench evaluator |
| PyPI | huggingface_hub | 1.29.0 | Apache-2.0 | [project](https://github.com/huggingface/huggingface_hub) | test/evaluation; LiveCodeBench evaluator |
| PyPI | idna | 3.19 | BSD-3-Clause | [project](https://github.com/kjd/idna/blob/master/HISTORY.md) | test/evaluation; LiveCodeBench evaluator |
| PyPI | iniconfig | 2.3.0 | MIT | [project](https://github.com/pytest-dev/iniconfig) | test/evaluation; API compatibility tests |
| PyPI | multidict | 6.7.1 | Apache License 2.0 | [project](https://github.com/aio-libs/multidict) | test/evaluation; LiveCodeBench evaluator |
| PyPI | multiprocess | 0.70.16 | BSD-3-Clause | [project](https://github.com/uqfoundation/multiprocess) | test/evaluation; LiveCodeBench evaluator |
| PyPI | numpy | 2.5.2 | BSD-3-Clause AND 0BSD AND MIT AND Zlib AND CC0-1.0 | [project](https://numpy.org) | test/evaluation; LiveCodeBench evaluator |
| PyPI | packaging | 26.2 | Apache-2.0 OR BSD-2-Clause | [project](https://packaging.pypa.io/) | test/evaluation; API compatibility tests |
| PyPI | packaging | 26.3 | Apache-2.0 OR BSD-2-Clause | [project](https://packaging.pypa.io/) | test/evaluation; LiveCodeBench evaluator |
| PyPI | pandas | 3.0.5 | BSD-3-Clause (installed metadata includes additional bundled notices) | [project](https://pandas.pydata.org) | test/evaluation; LiveCodeBench evaluator |
| PyPI | pluggy | 1.6.0 | MIT | **Unresolved** | test/evaluation; API compatibility tests |
| PyPI | propcache | 0.5.2 | Apache-2.0 | [project](https://github.com/aio-libs/propcache) | test/evaluation; LiveCodeBench evaluator |
| PyPI | pyarrow | 25.0.1 | Apache-2.0 | [project](https://arrow.apache.org/) | test/evaluation; LiveCodeBench evaluator |
| PyPI | pytest | 8.3.5 | MIT | [project](https://docs.pytest.org/en/stable/changelog.html) | test/evaluation; API compatibility tests |
| PyPI | python-dateutil | 2.9.0.post0 | Dual License | [project](https://github.com/dateutil/dateutil) | test/evaluation; LiveCodeBench evaluator |
| PyPI | PyYAML | 6.0.3 | MIT | [project](https://pyyaml.org/) | test/evaluation; LiveCodeBench evaluator |
| PyPI | requests | 2.34.2 | Apache-2.0 | [project](https://requests.readthedocs.io) | test/evaluation; LiveCodeBench evaluator |
| PyPI | six | 1.17.0 | MIT | [project](https://github.com/benjaminp/six) | test/evaluation; LiveCodeBench evaluator |
| PyPI | tqdm | 4.70.0 | MPL-2.0 AND MIT | [project](https://tqdm.github.io) | test/evaluation; LiveCodeBench evaluator |
| PyPI | typing_extensions | 4.16.0 | PSF-2.0 | [project](https://github.com/python/typing_extensions/issues) | test/evaluation; LiveCodeBench evaluator |
| PyPI | urllib3 | 2.7.0 | MIT | [project](https://github.com/urllib3/urllib3/blob/main/CHANGES.rst) | test/evaluation; LiveCodeBench evaluator |
| PyPI | xxhash | 4.0.1 | BSD-2-Clause | [project](https://github.com/ifduyue/python-xxhash) | test/evaluation; LiveCodeBench evaluator |
| PyPI | yarl | 1.24.5 | Apache-2.0 | [project](https://github.com/aio-libs/yarl) | test/evaluation; LiveCodeBench evaluator |
| vendored JS | @alexanderolsen/libsamplerate-js | 2.1.2 | MIT AND BSD-2-Clause | [project](https://github.com/aolsenjazz/libsamplerate-js) | shipped web artifact; local SHA-256 `ca6e162f194d3ee5a2d2cd3c3b49641d31e56845aeae507feca1cdf8aa8116cb` exactly matches `dist/libsamplerate.worklet.js` in the npm archive identified by integrity `sha512-pIXQDX/DZIgz6pKInUddDd6Tnq/s2E9g4ZITkKX60kxM/nAuZcxYa/z0y/jwJbjyp/oKe+8/qHLSqMzQ18xSiQ==`; root `NOTICE` preserves the bundled libsamplerate BSD-2-Clause attribution |
| vendored JS | @elevenlabs/convai-widget-embed | 0.16.3 | MIT | [project](https://github.com/elevenlabs/packages/tree/main/packages/convai-widget-embed) | shipped web artifact; derived from exact npm archive integrity `sha512-8meSwmCIqBhkG4hnUHxXjSeG7u6CgzPvEjP3HEGbbek30+vEs1T8uoyX345LyVrKaWh+3A/M+pEeFkWjNZlYsw==`; local SHA-256 `7316d8d048a3a963ab7986c8163b5e4deda966d1f03254566bf07183066038fc`; byte comparison found one deliberate substitution of the upstream jsDelivr worklet URL with `/docs/vendor/elevenlabs/libsamplerate.worklet-2.1.2.js`, with the remainder byte-equivalent to archive `dist/index.js` |

## Focused #957 dispositions and required preservation

The exact npm source archives establish MIT terms for `eval@0.1.8`,
`format@0.2.2`, `json-bignum@0.0.3`, `khroma@2.1.0`, and
`require-like@0.1.2`. The first, third, fourth, and fifth archives include
their MIT text; `format` declares MIT in its package metadata but omits a
separate license file. These packages are build inputs or operator/test tools
as classified above. MIT permission includes commercial redistribution, with
the condition that the upstream copyright and permission notice accompany
copies or substantial portions. Generated browser-bundle notice preservation
must therefore remain part of release legal review.

The following non-permissive or dual-license declarations are verified, but
verification is not legal clearance:

| Item | Distribution disposition |
| --- | --- |
| `elkjs@0.9.3` (EPL-2.0) | Docs build input whose code may enter the shipped browser bundle. Its exact npm archive contains EPL-2.0. Distribution remains subject to qualified legal review of EPL source-availability, license-copy, modification, and notices obligations; do not treat this inventory as clearance. |
| `dompurify@3.4.11` (MPL-2.0 OR Apache-2.0) | Mermaid dependency whose code may enter the shipped browser bundle. Its exact npm archive contains both license texts. A release owner must deliberately select and comply with one offered license; Apache-2.0 is available, but this document does not make that legal election or claim clearance. |
| `certifi@2026.7.22` (MPL-2.0) | Test/evaluation dependency only; not copied by normal binary or image recipes. Its exact source distribution says the bundled Mozilla CA certificate material is MPL-2.0. Any separate redistribution of the evaluator environment or certificate bundle requires MPL review and preservation. |
| `tqdm@4.70.0` (MPL-2.0 AND MIT) | Test/evaluation dependency only; not copied by normal binary or image recipes. Its exact source distribution applies MPL-2.0 generally and records MIT-covered historical material. Any separate redistribution of that environment requires both notices and qualified legal review. |

The installed `modernc.org/mathutil@v1.7.1` module contains BSD-3-Clause
terms, resolving the earlier classifier miss. It is linked transitively into
router builds through the modernc SQLite stack; the required binary-form
copyright, conditions, and disclaimer are preserved in root `NOTICE`.
`go-licenses` inability to inspect assembly in `golang.org/x/sys`,
`modernc.org/libc`, and `github.com/cespare/xxhash/v2` is a scanner limitation,
not evidence of additional dependencies or permission. Those modules retain
their separately inventoried licenses; assembly-derived or generated-code
provenance remains a release legal-review risk where the available module
license texts do not establish it.

The checked-in ElevenLabs widget is equivalent to the exact 0.16.3 npm
artifact except for one recorded worklet-URL substitution, and the checked-in
worklet exactly matches the 2.1.2 npm artifact. The exact archives establish
MIT for the widget and MIT plus bundled libsamplerate BSD-2-Clause terms for
the worklet. Preserve the ElevenLabs 2025 and Alexander Olsen 2021 MIT
copyright and permission notices with distributed substantial copies, and
preserve the libsamplerate BSD-2-Clause text in root `NOTICE`. Package licenses
grant copyright permissions but do not grant rights in ElevenLabs, Metrum, or
other trademarks.

For distributed substantial copies of the two vendored artifacts, preserve
the following archive-supplied MIT notices (the BSD-2-Clause component notice
is in root `NOTICE`):

> @elevenlabs/convai-widget-embed: Copyright (c) 2025 ElevenLabs.
>
> @alexanderolsen/libsamplerate-js: Copyright (c) 2021 Alexander Olsen.
>
> Permission is hereby granted, free of charge, to any person obtaining a copy
> of this software and associated documentation files (the "Software"), to deal
> in the Software without restriction, including without limitation the rights
> to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
> copies of the Software, and to permit persons to whom the Software is
> furnished to do so, subject to the following conditions:
>
> The above applicable copyright notice and this permission notice shall be
> included in all copies or substantial portions of the Software.
>
> THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
> IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
> FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
> AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
> LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
> OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
> SOFTWARE.

## Prominent unresolved and higher-risk review queue

The following records require a qualified human decision before public or customer distribution. “Unresolved” is deliberate: the installed or checked-in evidence did not state the field.

| Item | Practical risk / replacement availability |
| --- | --- |
| npm @esbuild/aix-ppc64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/android-arm 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/android-arm64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/android-x64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/darwin-arm64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/darwin-x64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/freebsd-arm64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/freebsd-x64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/linux-arm 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/linux-arm64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/linux-ia32 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/linux-loong64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/linux-mips64el 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/linux-ppc64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/linux-riscv64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/linux-s390x 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/netbsd-arm64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/netbsd-x64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/openbsd-arm64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/openbsd-x64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/openharmony-arm64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/sunos-x64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/win32-arm64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/win32-ia32 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @esbuild/win32-x64 0.28.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @napi-rs/lzma-linux-x64-gnu 1.5.1 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-android-arm-eabi 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-android-arm-eabi 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-android-arm64 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-android-arm64 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-darwin-arm64 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-darwin-arm64 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-darwin-x64 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-darwin-x64 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-freebsd-arm64 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-freebsd-arm64 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-freebsd-x64 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-freebsd-x64 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-arm-gnueabihf 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-arm-gnueabihf 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-arm-musleabihf 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-arm-musleabihf 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-arm64-gnu 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-arm64-gnu 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-arm64-musl 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-arm64-musl 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-loong64-gnu 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-loong64-gnu 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-loong64-musl 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-loong64-musl 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-ppc64-gnu 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-ppc64-gnu 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-ppc64-musl 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-ppc64-musl 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-riscv64-gnu 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-riscv64-gnu 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-riscv64-musl 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-riscv64-musl 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-s390x-gnu 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-linux-s390x-gnu 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-openbsd-x64 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-openbsd-x64 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-openharmony-arm64 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-openharmony-arm64 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-win32-arm64-msvc 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-win32-arm64-msvc 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-win32-ia32-msvc 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-win32-ia32-msvc 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-win32-x64-gnu 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-win32-x64-gnu 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-win32-x64-msvc 4.62.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm @rollup/rollup-win32-x64-msvc 4.62.4 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm fsevents 2.3.2 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm fsevents 2.3.3 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm robust-predicates 3.0.3 — license: Unlicense; homepage: https://github.com/mourner/robust-predicates | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| npm tailwindcss-animate 1.0.7 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| PyPI pluggy 1.6.0 — license: MIT; homepage: Unresolved | Confirm the exact governing terms, provenance, notice/source obligations, and compatibility. An equivalent permissively licensed replacement may reduce distribution risk, but availability was not established by this audit. |
| `elkjs@0.9.3`, `dompurify@3.4.11`, `certifi@2026.7.22`, and `tqdm@4.70.0` | Exact terms and scopes are recorded above. EPL/MPL compatibility and redistribution obligations still require a qualified legal disposition; no clearance is claimed. |
| Poppins and Geist font files under docs/admin static assets | Family-level OFL-1.1 terms are verified, but the checked-in WOFF2 hashes are not tied to an authoritative upstream revision and it is unknown whether renaming/subsetting created modified versions. Establish byte provenance and preserve the applicable copyright plus complete OFL-1.1 text before distribution. |
| Metrum logos and favicons under docs/admin static assets | Shipped assets lack a checked-in authorship or rights record. Confirm copyright ownership and separate trademark redistribution authority, or replace them; project history and filenames are not rights evidence. |
| Chart.js and generated browser bundles | Chart.js 4.5.1 is MIT, but normal builds compile dependency code into shipped docs/admin assets. Confirm generated artifacts preserve the complete Chart.js and other required upstream notices. The checked-in `internal/router/admindist/static/chart.umd.js` is repository-specific first-party tooltip code despite its filename. |
| Synthetic-looking outcome datasets | Not copied by release recipes, but authorship/source is not recorded. Confirm the exemplar text is original and not copied from a restricted benchmark before redistributing the source repository. |
| Model and external dataset references | No weights or external datasets are bundled, but examples/evaluators may trigger downloads with separate terms; unpinned model revisions and ambiguous dataset licensing remain open under `MODEL_LICENSES.md`. |

## Audit boundary and commands

The inventory covers modules owning packages reported by `go list -deps ./...` (cross-checked with ephemeral `go-licenses csv ./...`), every installed package from fresh `npm ci --ignore-scripts --no-audit --no-fund` runs for the three npm lockfiles, installed distributions from ephemeral frozen `uv sync` for API tests, and an ephemeral `uv pip install -r evaluators/livecodebench/requirements.txt`. The Harbor uv lock declares no external Python packages. The two checked-in vendored JavaScript files are separate records. npm records are classified by consuming surface: docs/admin dependencies are build inputs whose code may enter shipped browser bundles; the dashboard is an operator/development tool.

The Go classifier originally reported `modernc.org/mathutil` unresolved; the focused inspection above supersedes that result. Its assembly warnings remain bounded scanner limitations as described above. Optional platform npm packages remain actual lock/install records and are included.

No provider keys, router tokens, production configuration, prompts, model output, or other secrets were read or recorded. This document is not legal advice and does not replace preservation of required upstream license texts and notices in release artifacts.

## Focused remediation sources

Exact-version evidence was taken from the npm registry archives and their
included `package.json`/license files for
[`eval@0.1.8`](https://registry.npmjs.org/eval/-/eval-0.1.8.tgz),
[`format@0.2.2`](https://registry.npmjs.org/format/-/format-0.2.2.tgz),
[`json-bignum@0.0.3`](https://registry.npmjs.org/json-bignum/-/json-bignum-0.0.3.tgz),
[`khroma@2.1.0`](https://registry.npmjs.org/khroma/-/khroma-2.1.0.tgz),
[`require-like@0.1.2`](https://registry.npmjs.org/require-like/-/require-like-0.1.2.tgz),
[`elkjs@0.9.3`](https://registry.npmjs.org/elkjs/-/elkjs-0.9.3.tgz),
[`dompurify@3.4.11`](https://registry.npmjs.org/dompurify/-/dompurify-3.4.11.tgz),
[`@elevenlabs/convai-widget-embed@0.16.3`](https://registry.npmjs.org/@elevenlabs/convai-widget-embed/-/convai-widget-embed-0.16.3.tgz),
and
[`@alexanderolsen/libsamplerate-js@2.1.2`](https://registry.npmjs.org/@alexanderolsen/libsamplerate-js/-/libsamplerate-js-2.1.2.tgz).
Python evidence came from the exact PyPI source distributions for
[`certifi@2026.7.22`](https://files.pythonhosted.org/packages/source/c/certifi/certifi-2026.7.22.tar.gz)
and
[`tqdm@4.70.0`](https://files.pythonhosted.org/packages/source/t/tqdm/tqdm-4.70.0.tar.gz).
The `modernc.org/mathutil` disposition uses the `LICENSE` included in the local
Go module cache for the version authenticated by `go.sum`.
