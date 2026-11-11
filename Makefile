# edelweiss-ext-samba: Samba shares set up like on a NAS, an extension of edelweiss
# (https://github.com/Pukka922/edelweiss). README.md describes it.
GO ?= go
# The edelweiss checkout that make dev-sdk takes the SDKs from.
EDELWEISS_DIR ?= ../edelweiss
EDELWEISS_ABS = $(abspath $(EDELWEISS_DIR))
BINARY := edelweiss-ext-samba
ARCHES := amd64 arm64
# The root package and, once there is an internal/ (go vet and go test fail on ./internal/... without it), its packages.
GO_PKGS := . $(addsuffix /...,$(wildcard ./internal))
SDK_MODULE := github.com/Pukka922/edelweiss/sdk
ACTIONLINT := github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

# The version of a release, the tag without "v"; there is no default. release-check, release and gh-release need it.
# build passes it into the binaries when it is set; otherwise they report the placeholder 0.0.0-dev of manifest.json.
VERSION ?=
# The files of a release in dist/, besides checksums.txt: make release writes them, make gh-release uploads exactly these.
RELEASE_FILES = $(foreach arch,$(ARCHES),$(BINARY)_$(VERSION)_linux_$(arch))

# npm's record of the installed packages: npm ci writes it, and so does make dev-sdk.
UI_DEPS := ui/node_modules/.package-lock.json

.PHONY: build ui test test-alpine lint lint-workflows release-check release gh-release dev-sdk

# The UI (embedded by main.go), then static binaries for amd64 and arm64 in build/. A VERSION that reaches the binaries has
# to pass release-check first, however build is started (a binary with an invalid version panics at start); without a
# VERSION, build needs none.
build: $(if $(VERSION),release-check) ui
	mkdir -p build
	for arch in $(ARCHES); do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags "-s -w $(if $(VERSION),-X main.version=$(VERSION))" \
			-o build/$(BINARY)_linux_$$arch . || exit 1; \
	done

ui: $(UI_DEPS)
	cd ui && npm run build

test: $(UI_DEPS)
	$(GO) test -race $(GO_PKGS)
	cd ui && npm test
	sh scripts/release-guards-test.sh

# Tests against the real Samba of Alpine in throwaway containers (scripts/test-alpine.sh); they never run on
# this machine itself.
test-alpine:
	sh scripts/test-alpine.sh

lint: $(UI_DEPS) lint-workflows
	$(GO) vet $(GO_PKGS)
	$(if $(wildcard integration),$(GO) vet -tags integration ./integration)
	@test -z "$$(gofmt -l *.go $(wildcard internal integration))" || (gofmt -l *.go $(wildcard internal integration); echo "gofmt needed"; exit 1)
	cd ui && npx biome check .
	cd ui && npm run typecheck

lint-workflows:
	GOWORK=off $(GO) run $(ACTIONLINT) -shellcheck= -pyflakes=

# The tag is the only source of the version, so release-check does not compare it with manifest.json (make release puts
# it into the binaries and checks it there). It checks that VERSION is given, and then its format, by Go's rules for a
# version without the v: x.y.z or x.y.z-pre, numbers without leading zeros, no empty pre-release identifier and no
# leading zero in a numeric one, and no +build metadata (Go ignores it in a version tag). The value reaches the check
# through the environment, so the shell does not parse it as code, and the line after it only runs for a plain version.
# That protects the shell lines only: make itself still expands $(...) in a VERSION from the command line, so the
# tag-name check of the release workflow is the real boundary there.
# One pre-release identifier: digits without a leading zero, or characters of 0-9A-Za-z- with a letter or a hyphen.
SEMVER_ID := (0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)
SEMVER_RE := ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-$(SEMVER_ID)(\.$(SEMVER_ID))*)?$$
release-check: export RELEASE_CHECK_VERSION := $(VERSION)
release-check:
	@test -n "$$RELEASE_CHECK_VERSION" || { \
		echo "release-check: VERSION is required: the version of the release, the tag without v (make release VERSION=0.2.0)" >&2; \
		exit 1; }
	@export LC_ALL=C; bad=; \
	case "$$RELEASE_CHECK_VERSION" in \
		""|*[!0-9A-Za-z.-]*) bad=1 ;; \
		*) printf '%s\n' "$$RELEASE_CHECK_VERSION" | grep -Eq '$(SEMVER_RE)' || bad=1 ;; \
	esac; \
	test -z "$$bad" || { \
		printf 'release-check: VERSION=%s is not a version like 0.1.0 or 0.2.0-rc.1 (no v prefix, no +build metadata, no leading zeros)\n' \
			"$$RELEASE_CHECK_VERSION" >&2; exit 1; }
	@echo "release-check: $(VERSION) OK"

# The release files in dist/ (the release workflow uploads all of them), with the version of the tag in the binaries.
# Before it writes checksums.txt, it runs the binary for this machine with --edelweiss-manifest and compares the version
# it prints with VERSION (a binary that fails stops the release too): edelweiss refuses a binary whose manifest version
# differs from the version in its file name.
# The other architecture cannot be run here, and a machine that is not linux or not one of ARCHES cannot run any of the
# binaries; that stops the release instead of letting it go out unchecked (the release workflow runs on linux/amd64).
release: release-check build
	rm -rf dist
	mkdir -p dist
	for arch in $(ARCHES); do cp build/$(BINARY)_linux_$$arch dist/$(BINARY)_$(VERSION)_linux_$$arch || exit 1; done
	@os=$$($(GO) env GOHOSTOS); arch=$$($(GO) env GOHOSTARCH); known=; \
	case " $(ARCHES) " in *" $$arch "*) known=1 ;; esac; \
	test "$$os" = linux -a -n "$$known" || { \
		echo "release: the binaries cannot be run on this machine ($$os/$$arch) to check their version" >&2; exit 1; }; \
	bin=dist/$(BINARY)_$(VERSION)_linux_$$arch; \
	out=$$($$bin --edelweiss-manifest) || { echo "release: $$bin --edelweiss-manifest failed" >&2; exit 1; }; \
	got=$$(printf '%s\n' "$$out" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p'); \
	test "$$got" = "$(VERSION)" || { \
		echo "release: $$bin reports version \"$$got\", expected $(VERSION)" >&2; exit 1; }
	cd dist && sha256sum $(RELEASE_FILES) > checksums.txt

# Release workflow only (the publish job, which builds nothing): the GitHub release v<version> with the files of
# make release (needs gh and GH_TOKEN). dist/ has to hold exactly checksums.txt and RELEASE_FILES: the files match
# checksums.txt, which lists exactly them, and nothing else is there (no stale file of another version, no hidden file).
# A version with a pre-release part (a "-", like 0.2.0-rc.1) is published as a pre-release: the installer takes GitHub's
# latest release, which leaves pre-releases out, so an rc is not offered as an update.
# gh release create fails if the release already exists, so a rerun needs the release (or draft) deleted first, never
# the tag.
gh-release: release-check
	@test -f dist/checksums.txt || { echo "gh-release: dist/checksums.txt is missing; run make release first" >&2; exit 1; }
	cd dist && sha256sum -c checksums.txt
	@test "$$(ls -A dist | LC_ALL=C sort)" = "$$(printf '%s\n' checksums.txt $(RELEASE_FILES) | LC_ALL=C sort)" || { \
		echo "gh-release: dist/ has to hold exactly checksums.txt and $(RELEASE_FILES); run make release first" >&2; exit 1; }
	@test "$$(sed 's/^[0-9a-f]\{64\} [ *]//' dist/checksums.txt | LC_ALL=C sort)" = "$$(printf '%s\n' $(RELEASE_FILES) | LC_ALL=C sort)" || { \
		echo "gh-release: dist/checksums.txt has to list exactly $(RELEASE_FILES); run make release first" >&2; exit 1; }
	gh release create "v$(VERSION)" --verify-tag --title "$(BINARY) $(VERSION)" --generate-notes \
		$(if $(findstring -,$(VERSION)),--prerelease) $(addprefix dist/,checksums.txt $(RELEASE_FILES))

ifneq ($(wildcard ui/package-lock.json),)
$(UI_DEPS): ui/package-lock.json ui/package.json
	cd ui && npm ci
else
$(UI_DEPS):
	@echo "ui/node_modules is missing, and so is ui/package-lock.json, which npm ci installs from:" >&2
	@echo "run npm install in ui/ to create it, or make dev-sdk to build against an edelweiss checkout (README.md, Development)" >&2
	@exit 1
endif

# Develop against the edelweiss checkout in EDELWEISS_DIR instead of the published SDKs: a go.work (not checked in)
# whose replace points the Go SDK to that checkout, and that checkout's JS SDK tarball in ui/node_modules
# (ui/package.json stays unchanged, no package-lock.json is written). Needs no access to github.com.
dev-sdk:
	@test -f "$(EDELWEISS_ABS)/sdk/go.mod" -a -f "$(EDELWEISS_ABS)/web/sdk/package.json" || { \
		echo "dev-sdk: no edelweiss checkout at $(EDELWEISS_ABS) (EDELWEISS_DIR=$(EDELWEISS_DIR)):" >&2; \
		echo "clone github.com/Pukka922/edelweiss next to this repository or set EDELWEISS_DIR=<path>" >&2; \
		exit 1; }
	rm -f go.work go.work.sum
	$(GO) work init .
	$(GO) work edit -replace $(SDK_MODULE)=$(EDELWEISS_ABS)/sdk
	$(MAKE) -C $(EDELWEISS_ABS) sdk-pack VERSION=
	v=$$(sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' $(EDELWEISS_ABS)/web/sdk/package.json) && \
		cd ui && npm install --no-save --no-package-lock --no-audit --no-fund $(EDELWEISS_ABS)/dist/edelweiss-sdk-$$v.tgz
