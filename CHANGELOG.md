# Changelog

## [0.3.0](https://github.com/spacingmind/claude-agent-sdk-go/compare/v0.2.1...v0.3.0) (2026-08-28)


### Features

* CLI binary discovery, 3-stage close escalation, version check (phase 4c) ([969fa5f](https://github.com/spacingmind/claude-agent-sdk-go/commit/969fa5f6ea5a01fd9fafd9d919fdb53875c2b36e))
* **messages:** message & content-block parity with upstream SDKs ([788a98a](https://github.com/spacingmind/claude-agent-sdk-go/commit/788a98a30410a8c696d53bfc490d6b36f5ad886c))


### Bug Fixes

* CI trigger branch name (main, not master) ([f204576](https://github.com/spacingmind/claude-agent-sdk-go/commit/f204576f53ad174e1e372011ee2085ac882c9de2))
* correct release-please tag format, enforce PR title convention ([#9](https://github.com/spacingmind/claude-agent-sdk-go/issues/9)) ([a9b45f0](https://github.com/spacingmind/claude-agent-sdk-go/commit/a9b45f064b4c45de7db49fa2312ffb6dfb32ea6d))
* never show raw git author name/email in changelog entries ([a1d36e9](https://github.com/spacingmind/claude-agent-sdk-go/commit/a1d36e96b6ad27cc49916674808177e942d68475))
* remove skip-github-release, it breaks release-please's tag tracking ([5dad4c9](https://github.com/spacingmind/claude-agent-sdk-go/commit/5dad4c9259f5e9a5efb7ead95a62241f12c2b4e7))
* suppress Close() wait error on SIGTERM stage too, not just SIGKILL ([2bdef98](https://github.com/spacingmind/claude-agent-sdk-go/commit/2bdef9896461816b15bd8259438bb3fdafc86655))

## [0.2.1](https://github.com/spacingmind/claude-agent-sdk-go/compare/claude-agent-sdk-go-v0.2.0...claude-agent-sdk-go-v0.2.1) (2026-08-28)


### Bug Fixes

* remove skip-github-release, it breaks release-please's tag tracking ([5dad4c9](https://github.com/spacingmind/claude-agent-sdk-go/commit/5dad4c9259f5e9a5efb7ead95a62241f12c2b4e7))

## [0.2.0](https://github.com/spacingmind/claude-agent-sdk-go/compare/claude-agent-sdk-go-v0.1.0...claude-agent-sdk-go-v0.2.0) (2026-08-28)


### Features

* CLI binary discovery, 3-stage close escalation, version check (phase 4c) ([969fa5f](https://github.com/spacingmind/claude-agent-sdk-go/commit/969fa5f6ea5a01fd9fafd9d919fdb53875c2b36e))
* **messages:** message & content-block parity with upstream SDKs ([788a98a](https://github.com/spacingmind/claude-agent-sdk-go/commit/788a98a30410a8c696d53bfc490d6b36f5ad886c))


### Bug Fixes

* CI trigger branch name (main, not master) ([f204576](https://github.com/spacingmind/claude-agent-sdk-go/commit/f204576f53ad174e1e372011ee2085ac882c9de2))
* never show raw git author name/email in changelog entries ([a1d36e9](https://github.com/spacingmind/claude-agent-sdk-go/commit/a1d36e96b6ad27cc49916674808177e942d68475))
* suppress Close() wait error on SIGTERM stage too, not just SIGKILL ([2bdef98](https://github.com/spacingmind/claude-agent-sdk-go/commit/2bdef9896461816b15bd8259438bb3fdafc86655))
