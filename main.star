#!/usr/bin/env lucicfg

lucicfg.check_version("1.35.3", "Please update depot_tools")

lucicfg.config(
    config_dir = "generated",
    tracked_files = ["*.cfg"],
    fail_on_warnings = True,
    lint_checks = ["default", "-module-docstring"],
)

luci.project(
    name = "golang",
    buildbucket = "cr-buildbucket.appspot.com",
    logdog = "luci-logdog.appspot.com",
    milo = "luci-milo.appspot.com",
    notify = "luci-notify.appspot.com",
    scheduler = "luci-scheduler.appspot.com",
    swarming = "chromium-swarm.appspot.com",
    bindings = [
        # Admin permissions.
        luci.binding(
            roles = [
                # Allow owners to submit any task in any pool.
                "role/swarming.poolOwner",
                "role/swarming.poolUser",
                "role/swarming.poolViewer",
                "role/swarming.taskTriggerer",
                # Allow owners to trigger and cancel LUCI Scheduler jobs.
                "role/scheduler.owner",
                # Allow owners to trigger and cancel any build.
                "role/buildbucket.owner",
                # Allow owners to read/validate/reimport the project configs.
                "role/configs.developer",

                # Allow owners to create/edit ResultDB invocations (for local result_adapter development).
                # TODO(dmitshur): Remove or move to AOD after it stops being actively used.
                "role/resultdb.invocationCreator",
            ],
            groups = "mdb/golang-luci-admin",
        ),

        # Allow task service accounts to spawn builds.
        luci.binding(
            roles = "role/buildbucket.triggerer",
            users = [
                "coordinator-builder@golang-ci-luci.iam.gserviceaccount.com",
                "security-coordinator-builder@golang-ci-luci.iam.gserviceaccount.com",
            ],
        ),

        # Allow task service accounts to validate configurations.
        #
        # This is fine, since all our configurations for this project (even for internal
        # builders) are public.
        luci.binding(
            roles = "role/configs.validator",
            users = [
                "public-worker-builder@golang-ci-luci.iam.gserviceaccount.com",
            ],
        ),

        # luci-analysis permissions.
        #
        # Note on security: this appears to open up reading luci-analysis to everyone
        # for the entire project, but access to test results and cluster definitions
        # (test names and failure reasons) is gated by access to the corresponding
        # realm. What this is actually granting access to is cluster IDs, which are
        # hashes of cluster definitions. In other words, all users can take a failure
        # they have access to and find which clusters in the project correspond to it,
        # but they can only see the details of the cluster if they have access to that
        # realm.
        #
        # Note that this also grants access to rule definitions for the whole project.
        # This is OK for now since our security realm is just presubmit, and we're unlikely
        # to write rules corresponding to failures in that realm that aren't public
        # anyway. (And again, test result details are all still hidden.)
        #
        # If in the future we want to change this, there's a role/analysis.limitedReader
        # role for limiting access to rule definitions as well.
        luci.binding(
            roles = [
                "role/analysis.reader",
            ],
            groups = "all",
        ),
        luci.binding(
            # Allow approvers to mutate luci-analysis state.
            roles = ["role/analysis.editor"],
            groups = ["project-golang-approvers"],
        ),
        luci.binding(
            # Allow authenticated users to run analysis queries in public realms.
            # This policy may seem a bit strange, but the idea is to allow community
            # members to run failure analyses while still keeping a record of
            # who did it (by making them log in) to identify misuse.
            # The Chromium project does this and hasn't had any problems yet.
            roles = ["role/analysis.queryUser"],
            groups = ["authenticated-users"],
        ),
    ],
    acls = [
        acl.entry(
            roles = acl.PROJECT_CONFIGS_READER,
            groups = "all",
        ),
    ],
)

# Per-service tweaks.
luci.logdog(gs_bucket = "logdog-golang-archive")

# Realms for public buckets and Swarming pools.
PUBLIC_REALMS = [
    luci.realm(name = "pools/ci"),
    luci.realm(name = "pools/ci-workers"),
    luci.realm(name = "pools/try"),
    luci.realm(name = "pools/try-workers"),
    luci.realm(name = "pools/shared-workers"),
    luci.bucket(name = "try"),
    luci.bucket(name = "try-workers"),
    luci.bucket(name = "ci"),
    luci.bucket(name = "ci-workers"),
]

SECURITY_REALMS = [
    luci.realm(name = "pools/security-try"),
    luci.realm(name = "pools/security-try-workers"),
    luci.bucket(name = "security-try"),
    luci.bucket(name = "security-try-workers"),
]

luci.realm(name = "pools/prod")

# Allow everyone to see public builds, pools, bots, and tasks.
# WARNING: this doesn't do much for Swarming entities -- chromium-swarm
# has a global allow-all ACL that supersedes us. Private realms run in
# chrome-swarming.
luci.binding(
    roles = [
        "role/swarming.poolViewer",
        "role/buildbucket.reader",
    ],
    realm = PUBLIC_REALMS,
    groups = "all",
)

# may-start-trybots grants the permission to trigger public builds.
luci.binding(
    roles = ["role/buildbucket.triggerer"],
    realm = PUBLIC_REALMS,
    groups = ["project-golang-may-start-trybots"],
)

# Allow security release participants to see and trigger security builds, etc.
# WARNING: similar to above, chrome-swarming is open to all Googlers.
luci.binding(
    roles = ["role/swarming.poolViewer", "role/buildbucket.reader", "role/buildbucket.triggerer"],
    realm = SECURITY_REALMS,
    groups = ["mdb/golang-security-policy", "mdb/golang-release-eng-policy"],
)

# Allow x/build/cmd/makemac to list security bots.
luci.binding(
    roles = "role/swarming.poolViewer",
    realm = SECURITY_REALMS,
    users = "makemac@symbolic-datum-552.iam.gserviceaccount.com",
)

# Allow users with the taskTriggerer role to impersonate the service accounts.
luci.binding(
    roles = "role/swarming.taskServiceAccount",
    realm = PUBLIC_REALMS,
    users = "coordinator-builder@golang-ci-luci.iam.gserviceaccount.com",
)

# Allow gomoteserver to run Swarming tasks and BuildBucket builds in public realms.
luci.binding(
    roles = [
        "role/buildbucket.triggerer",
        "role/swarming.poolUser",
        "role/swarming.taskTriggerer",
    ],
    realm = PUBLIC_REALMS,
    users = "gomoteserver@symbolic-datum-552.iam.gserviceaccount.com",
)

# Allow relui to run Swarming tasks and BuildBucket builds in both security realms
# (for Go releases) and public realms (for x repo tagging, etc.)
luci.binding(
    roles = [
        "role/swarming.taskTriggerer",
        "role/swarming.poolUser",
        "role/buildbucket.triggerer",
    ],
    realm = PUBLIC_REALMS + SECURITY_REALMS,
    users = "relui-prod@symbolic-datum-552.iam.gserviceaccount.com",
)

# Define relui-tasks as a LUCI service account. This is normally handled
# by a luci.builder call, but this isn't a standard builder account.
luci.binding(
    roles = "role/swarming.taskServiceAccount",
    realm = PUBLIC_REALMS + SECURITY_REALMS,
    users = [
        "relui-tasks@symbolic-datum-552.iam.gserviceaccount.com",
    ],
)

# This is the cipd package name and version where the recipe bundler will put
# the built recipes. This line makes it the default value for all `luci.recipe`
# invocations in this configuration.
#
# The recipe bundler sets CIPD refs equal in name to the git refs that it
# processed the recipe code from.
#
# Note: This will cause all recipe commits to automatically deploy as soon
# as the recipe bundler compiles them from your refs/heads/luci-config branch.
luci.recipe.defaults.cipd_package.set("golang/recipe_bundles/go.googlesource.com/build")
luci.recipe.defaults.cipd_version.set("refs/heads/luci-config")
luci.recipe.defaults.use_python3.set(True)

def define_environment(gerrit_host, swarming_host, bucket, coordinator_sa, worker_sa, has_shared_pool, low_capacity, known_issue, priority = None):
    return struct(
        gerrit_host = gerrit_host,
        swarming_host = swarming_host + ".appspot.com",
        bucket = bucket,
        worker_bucket = bucket + "-workers",
        shadow_bucket = bucket + ".shadow",
        worker_shadow_bucket = bucket + "-workers.shadow",
        coordinator_sa = coordinator_sa + "@golang-ci-luci.iam.gserviceaccount.com",
        coordinator_pool = "luci.golang.%s" % bucket,
        worker_sa = worker_sa + "@golang-ci-luci.iam.gserviceaccount.com",
        worker_pool = "luci.golang.%s-workers" % bucket,
        shared_worker_pool = "luci.golang.shared-workers" if has_shared_pool else "",
        low_capacity_hosts = low_capacity,
        known_issue_builder_types = known_issue,
        priority = priority,
    )

# FIRST_CLASS_PORTS lists all ports (goos-goarch pairs) that are considered to have
# first-class support by the Go project. See https://go.dev/wiki/PortingPolicy#first-class-ports.
FIRST_CLASS_PORTS = [
    "darwin-amd64",
    "darwin-arm64",
    "linux-386",
    "linux-amd64",
    "linux-arm",
    "linux-arm64",
    "windows-386",
    "windows-amd64",
]

# is_first_class if the goos-goarch pair is a first-class port.
# This must not be a host, but part of the builder type (e.g. js-wasm, not its host).
def is_first_class(goos, goarch):
    return goos + "-" + goarch in FIRST_CLASS_PORTS

# GOOGLE_LOW_CAPACITY_HOSTS are low-capacity hosts that happen to be operated
# by Google, so we can rely on them being available.
GOOGLE_LOW_CAPACITY_HOSTS = [
    "darwin-amd64_11",
    "darwin-amd64_12",
    "darwin-amd64_13",
    "darwin-amd64_14",
    "darwin-arm64_11",
    "darwin-arm64_12",
    "darwin-arm64_13",
    "darwin-arm64_14",
    "darwin-arm64_15",
    "linux-arm",
    "windows-arm64",
]

# TBD_CAPACITY_HOSTS lists "hosts" that whose capacity is yet to be determined.
# When the work to add a host is underway, its entry should either move to the
# LOW_CAPACITY_HOSTS list below, or removed if it's not low-capacity.
TBD_CAPACITY_HOSTS = [
    "android-386",
    "android-amd64",
    "android-arm",
    "android-arm64",
    "dragonfly-amd64",
    "freebsd-386",
    "freebsd-arm",
    "freebsd-arm64",
    "illumos-amd64",
    "ios-amd64",
    "ios-arm64",
    "linux-mips",
    "linux-mips64",
    "linux-mipsle",
    "netbsd-386",
    "netbsd-amd64",
]

# LOW_CAPACITY_HOSTS lists "hosts" that have fixed, relatively low capacity.
# They need to match the builder type, excluding any run mods.
LOW_CAPACITY_HOSTS = GOOGLE_LOW_CAPACITY_HOSTS + TBD_CAPACITY_HOSTS + [
    "aix-ppc64",
    "freebsd-riscv64",
    "linux-loong64",
    "linux-mips64le",
    "linux-ppc64_power10",
    "linux-ppc64_power8",
    "linux-ppc64le_power10",
    "linux-ppc64le_power8",
    "linux-ppc64le_power9",
    "linux-riscv64",
    "linux-s390x",
    "netbsd-arm",
    "netbsd-arm64",
    "openbsd-arm",
    "openbsd-arm64",
    "openbsd-ppc64",
    "openbsd-riscv64",
    "plan9-386",
    "plan9-amd64",
    "plan9-arm",
    "solaris-amd64",
]

# HOST_CONTACT_EMAILS is the contact information for a particular host.
# These email addresses will recieve notifications about infra failures
# on certain hosts.
HOST_CONTACT_EMAILS = {
    "netbsd-arm": ["bsiegert@google.com"],
    "netbsd-arm64": ["bsiegert@google.com"],
}

HOST_NOTIFIERS = {
    host: luci.notifier(
        name = host + "-infra",
        on_new_status = ["INFRA_FAILURE"],
        notify_emails = emails,
    )
    for host, emails in HOST_CONTACT_EMAILS.items()
}

# SLOW_HOSTS lists "hosts" who are known to run slower than our typical fast
# high-capacity machines. It is a mapping of the host to a base test timeout
# scaling factor; run_mods may multiply this scaling factor further. It also
# affects the decision of whether to include a builder in presubmit testing
# by default (slow high-capacity hosts aren't included).
#
# Optionally, a slow host may also limit its repo@go_branch_short scope if there's
# no better alternative. This has the downside of reducing coverage for the port.
SLOW_HOSTS = {
    "darwin-amd64": struct(scale = 2),  # see go.dev/issue/65040
    "freebsd-riscv64": struct(scale = 4),
    "linux-ppc64_power10": struct(scale = 2),
    "linux-ppc64_power8": struct(scale = 2),
    "linux-ppc64le_power10": struct(scale = 2),
    "linux-ppc64le_power8": struct(scale = 2),
    "linux-ppc64le_power9": struct(scale = 2),
    "linux-riscv64": struct(scale = 2),
    "linux-s390x": struct(scale = 2),  # see go.dev/issue/60413
    "netbsd-arm": struct(
        scale = 5,
        scope = ["go", "net", "sys"],  # not tools; see go.dev/issue/72061#issuecomment-2695226251
    ),
    "netbsd-arm64": struct(scale = 2),
    "openbsd-amd64": struct(scale = 4),
    "openbsd-arm": struct(scale = 5),
    "openbsd-arm64": struct(scale = 5),
    "openbsd-ppc64": struct(scale = 3),
    "openbsd-riscv64": struct(scale = 4),
    "windows-arm64": struct(scale = 2),
}

# host_timeout_scale returns the default test timeout scale for a given host.
def host_timeout_scale(host):
    if host in SLOW_HOSTS:
        return SLOW_HOSTS[host].scale
    return 1

# DEFAULT_HOST_SUFFIX defines the default host suffixes for builder types which
# do not specify one.
DEFAULT_HOST_SUFFIX = {
    "darwin-amd64": "14",
    "darwin-arm64": "15",
    "freebsd-amd64": "14.2",
    "linux-amd64": "debian11",
    "linux-arm64": "debian12",
    "openbsd-386": "7.6",
    "openbsd-amd64": "7.6",
    "windows-386": "10",
    "windows-amd64": "10",
}

# The prod bucket will include builders which work on post-commit code and
# generate executable artifacts used by other users or machines.
luci.bucket(name = "prod")

# A list with builders in "prod" bucket.
luci.list_view(
    name = "prod",
)

# BUILDER_TYPES lists possible builder types.
#
# A builder type is a combination of a GOOS and GOARCH, an optional suffix that
# specifies the OS version or CPU version, and a series of run-time modifications
# (listed in RUN_MODS).
#
# The format of a builder type is thus $GOOS-$GOARCH(_suffix)?(-$RUN_MOD)*.
BUILDER_TYPES = [
    "aix-ppc64",
    "android-386",
    "android-amd64",
    "android-arm",
    "android-arm64",
    "darwin-amd64-longtest",
    "darwin-amd64-nocgo",
    "darwin-amd64-race",
    "darwin-amd64_11",
    "darwin-amd64_12",
    "darwin-amd64_13",
    "darwin-amd64_14",
    "darwin-arm64_11",
    "darwin-arm64_12",
    "darwin-arm64_13",
    "darwin-arm64_14",
    "darwin-arm64_15",
    "darwin-arm64-longtest",
    "darwin-arm64-race",
    "dragonfly-amd64",
    "freebsd-386",
    "freebsd-amd64",
    "freebsd-amd64-race",
    "freebsd-amd64_14.1",
    "freebsd-arm",
    "freebsd-arm64",
    "freebsd-riscv64",
    "illumos-amd64",
    "ios-amd64",
    "ios-arm64",
    "js-wasm",
    "linux-386",
    "linux-386-clang15",
    "linux-386_debiansid",
    "linux-386-longtest",
    "linux-386-nogreenteagc",
    "linux-386-sizespecializedmalloc",
    "linux-386-softfloat",
    "linux-amd64",
    "linux-amd64-asan-clang15",
    "linux-amd64-boringcrypto",
    "linux-amd64-clang15",
    "linux-amd64-goamd64v3",
    "linux-amd64-longtest",
    "linux-amd64-longtest-race",
    "linux-amd64-longtest-noswissmap",
    "linux-amd64-longtest-aliastypeparams",
    "linux-amd64-misccompile",
    "linux-amd64-msan-clang15",
    "linux-amd64-newinliner",
    "linux-amd64-nocgo",
    "linux-amd64-nogreenteagc",
    "linux-amd64-noopt",
    "linux-amd64-race",
    "linux-amd64-racecompile",
    "linux-amd64-runtimefreegc",
    "linux-amd64-sizespecializedmalloc",
    "linux-amd64-ssacheck",
    "linux-amd64-staticlockranking",
    "linux-amd64-tiplang",
    "linux-amd64-typesalias",
    "linux-amd64-aliastypeparams",
    "linux-amd64_avx512",
    "linux-amd64_c2s16-perf_pgo_vs_oldest_stable",
    "linux-amd64_c2s16-perf_vs_gopls_0_11",
    "linux-amd64_c2s16-perf_vs_parent",
    "linux-amd64_c2s16-perf_vs_release",
    "linux-amd64_c2s16-perf_vs_tip",
    "linux-amd64_c2s16-perf_vs_oldest_stable",
    "linux-amd64_c3h88-perf_pgo_vs_oldest_stable",
    "linux-amd64_c3h88-perf_vs_parent",
    "linux-amd64_c3h88-perf_vs_release",
    "linux-amd64_c3h88-perf_vs_tip",
    "linux-amd64_c3h88-perf_vs_oldest_stable",
    "linux-amd64_debiansid",
    "linux-amd64_docker",
    "linux-arm",
    "linux-arm64",
    "linux-arm64-asan-clang15",
    "linux-arm64-boringcrypto",
    "linux-arm64-clang15",
    "linux-arm64-nogreenteagc",
    "linux-arm64-longtest",
    "linux-arm64-msan-clang15",
    "linux-arm64-race",
    "linux-arm64-sizespecializedmalloc",
    "linux-arm64_c4as16-perf_vs_gopls_0_11",
    "linux-arm64_c4as16-perf_vs_parent",
    "linux-arm64_c4as16-perf_vs_release",
    "linux-arm64_c4as16-perf_vs_tip",
    "linux-arm64_c4as16-perf_vs_oldest_stable",
    "linux-arm64_c4ah72-perf_vs_parent",
    "linux-arm64_c4ah72-perf_vs_release",
    "linux-arm64_c4ah72-perf_vs_tip",
    "linux-arm64_c4ah72-perf_vs_oldest_stable",
    "linux-arm64_debian13",
    "linux-loong64",
    "linux-mips",
    "linux-mips64",
    "linux-mips64le",
    "linux-mipsle",
    "linux-ppc64_power10",
    "linux-ppc64_power8",
    "linux-ppc64le_power10",
    "linux-ppc64le_power8",
    "linux-ppc64le_power9",
    "linux-riscv64",
    "linux-s390x",
    "linux-s390x-race",
    "netbsd-386",
    "netbsd-amd64",
    "netbsd-arm",
    "netbsd-arm64",
    "openbsd-386",
    "openbsd-amd64",
    "openbsd-arm",
    "openbsd-arm64",
    "openbsd-ppc64",
    "openbsd-riscv64",
    "plan9-386",
    "plan9-amd64",
    "plan9-arm",
    "solaris-amd64",
    "wasip1-wasm_wasmtime",
    "wasip1-wasm_wazero",
    "windows-386",
    "windows-amd64",
    "windows-amd64-longtest",
    "windows-amd64-race",
    "windows-arm64",
]

def known_issue(issue_number, skip_x_repos = False, hide_from_presubmit = True):
    return struct(
        issue_number = issue_number,
        skip_x_repos = skip_x_repos,  # Whether to skip defining builders for x/ repos.
        hide_from_presubmit = hide_from_presubmit,
    )

KNOWN_ISSUE_BUILDER_TYPES = {
    "linux-arm64_debian13": known_issue(issue_number = 74985, hide_from_presubmit = False),
    "freebsd-amd64_14.1": known_issue(issue_number = 72030, skip_x_repos = True, hide_from_presubmit = False),
    "linux-arm64-msan-clang15": known_issue(issue_number = 71614),
    "plan9-amd64": known_issue(issue_number = 63600, hide_from_presubmit = False),
    "freebsd-riscv64": known_issue(issue_number = 73568, hide_from_presubmit = False),
    "openbsd-riscv64": known_issue(issue_number = 73569, hide_from_presubmit = False),

    # The known issue for these builder types tracks the work of starting to add them.
    # Skip the builder definitions for x/ repos to reduce noise.
    # Once the builder is added and starts working in the main repo, x/ repos can be unskipped
    # and it can be shown in the presubmit list.
    "aix-ppc64": known_issue(issue_number = 67299, skip_x_repos = True),
    "android-386": known_issue(issue_number = 61097, skip_x_repos = True),
    "android-amd64": known_issue(issue_number = 61097, skip_x_repos = True),
    "android-arm": known_issue(issue_number = 61097, skip_x_repos = True),
    "android-arm64": known_issue(issue_number = 61097, skip_x_repos = True),
    "dragonfly-amd64": known_issue(issue_number = 61092, skip_x_repos = True),
    "freebsd-386": known_issue(issue_number = 60468, skip_x_repos = True),
    "freebsd-arm": known_issue(issue_number = 67300, skip_x_repos = True),
    "freebsd-arm64": known_issue(issue_number = 67301, skip_x_repos = True),
    "illumos-amd64": known_issue(issue_number = 67302, skip_x_repos = True),
    "ios-amd64": known_issue(issue_number = 42177, skip_x_repos = True),
    "ios-arm64": known_issue(issue_number = 66360, skip_x_repos = True),
    "linux-mips": known_issue(issue_number = 67303, skip_x_repos = True),
    "linux-mips64": known_issue(issue_number = 67305, skip_x_repos = True),
    "linux-mipsle": known_issue(issue_number = 67304, skip_x_repos = True),
    "netbsd-386": known_issue(issue_number = 61120, skip_x_repos = True),
    "netbsd-amd64": known_issue(issue_number = 61121, skip_x_repos = True),
    "openbsd-386": known_issue(issue_number = 61122, skip_x_repos = True),
    "openbsd-arm": known_issue(issue_number = 67103, skip_x_repos = True),
    "openbsd-arm64": known_issue(issue_number = 67104, skip_x_repos = True),
}
SECURITY_KNOWN_ISSUE_BUILDER_TYPES = dict(KNOWN_ISSUE_BUILDER_TYPES)
SECURITY_KNOWN_ISSUE_BUILDER_TYPES.update({
})

# NO_NETWORK_BUILDERS are a subset of builder types
# where we require the no-network check to run.
NO_NETWORK_BUILDERS = [
    "linux-386",
    "linux-amd64",
]

# MAIN_BRANCH_NAME is the name of the main branch for every repository. This
# exists so we can change it more easily in the future, and avoid propagating
# the existing one everywhere.
MAIN_BRANCH_NAME = "master"

# LATEST_GO and SECOND_GO are moving targets that correspond to the latest
# supported major Go release, and second latest supported major Go release.
# See the Go Release Policy at go.dev/doc/devel/release#policy.
#
# NEXTUP_GO is the next upcoming release. It's used only at the end of the
# release cycle, when a new release branch is cut, before it's promoted to
# being used for remaining minor Go releases.
#
# These need to be updated every 6 months after a major Go release is made.
# Keep bootstrap versions tracked inside GO_BRANCHES in mind when updating.
NEXTUP_GO = "go1.26"
LATEST_GO = "go1.25"
SECOND_GO = "go1.24"

# GO_BRANCHES lists the branches of the "go" project to build and test against.
# Keys in this map are shortened aliases while values are the git branch name.
GO_BRANCHES = {
    "gotip": struct(branch = MAIN_BRANCH_NAME, bootstrap = "1.24.6"),
    NEXTUP_GO: struct(branch = "release-branch." + NEXTUP_GO, bootstrap = "1.24.6"),
    LATEST_GO: struct(branch = "release-branch." + LATEST_GO, bootstrap = "1.22.6"),
    SECOND_GO: struct(branch = "release-branch." + SECOND_GO, bootstrap = "1.22.6"),
}

# INTERNAL_GO_BRANCHES mirrors GO_BRANCHES, but defines the branches to build
# and test against for the go-internal/go repository.
INTERNAL_GO_BRANCHES = {
    # Testing of internal release patches are initially based on the "public"
    # branch, which tracks MAIN_BRANCH_NAME in the public repository, and
    # branches which are cut immediately before the release in order to submit
    # the patches. These branches take the form "public-release-{month}-{year}".
    # See go/go-security-release-workflow for a discussion of this.
    "gotip": struct(branch_regexps = ["public", "public-release-[a-z]+-\\d+"]),
    # The internal release branches are per-individual release, rather than
    # per-major version, since we create them specially for each individual
    # release, and want to maintain that history. We use a regexp like
    # "internal-release-branch.go1.23.+" to match all the branches so that
    # we don't need to manually update the config for each next minor or RC.
    NEXTUP_GO: struct(branch_regexps = ["internal-" + GO_BRANCHES[NEXTUP_GO].branch + ".+", "private-internal-branch." + NEXTUP_GO + "-vendor"]),
    LATEST_GO: struct(branch_regexps = ["internal-" + GO_BRANCHES[LATEST_GO].branch + ".+", "private-internal-branch." + LATEST_GO + "-vendor"]),
    SECOND_GO: struct(branch_regexps = ["internal-" + GO_BRANCHES[SECOND_GO].branch + ".+", "private-internal-branch." + SECOND_GO + "-vendor"]),
}

# TOOLS_GO_BRANCHES are Go branches that aren't used for project-wide testing
# because they're out of scope per https://go.dev/doc/devel/release#policy,
# but are used by only by the golang.org/x/tools repository for a while longer.
#
# TODO(go.dev/issue/75338): Follow up as needed.
TOOLS_GO_BRANCHES = {
    "go1.23": struct(branch = "release-branch.go1.23", bootstrap = "1.20.6"),
}

# We set build priorities by environment. These should always be lower than the
# build priority for gomote requests, which is 20 (lower number means higher priority).
PRIORITY = struct(
    GOMOTE = 20,
    PRESUBMIT = 30,
    POSTSUBMIT = 40,
)

# The try bucket will include builders which work on pre-commit or pre-review
# code.
PUBLIC_TRY_ENV = define_environment("go", "chromium-swarm", "try", "coordinator-builder", "public-worker-builder", True, LOW_CAPACITY_HOSTS, KNOWN_ISSUE_BUILDER_TYPES, priority = PRIORITY.PRESUBMIT)

# The ci bucket will include builders which work on post-commit code.
PUBLIC_CI_ENV = define_environment("go", "chromium-swarm", "ci", "coordinator-builder", "public-worker-builder", True, LOW_CAPACITY_HOSTS, KNOWN_ISSUE_BUILDER_TYPES, priority = PRIORITY.POSTSUBMIT)

# The security-try bucket is for builders that test unreviewed, embargoed
# security fixes.
SECURITY_TRY_ENV = define_environment("go-internal", "chrome-swarming", "security-try", "security-coordinator-builder", "security-worker-builder", False, LOW_CAPACITY_HOSTS, SECURITY_KNOWN_ISSUE_BUILDER_TYPES, priority = PRIORITY.PRESUBMIT)

def define_public_shadow_buckets(env):
    if env.swarming_host != "chromium-swarm.appspot.com":
        fail("only the chromium-swarm instance is known to be intended for public builds, but env.swarming_host is %s" % env.swarming_host)

    all_pools = [env.coordinator_pool, env.worker_pool]
    if env.shared_worker_pool:
        all_pools.append(env.shared_worker_pool)

    for bucket, shadow_bucket in [(env.bucket, env.shadow_bucket), (env.worker_bucket, env.worker_shadow_bucket)]:
        luci.bucket(
            shadows = bucket,
            name = shadow_bucket,
            dynamic = True,
            # Explicitly set bucket constraints for shadow buckets. These are populated
            # for each real bucket implicitly via luci.builder(...), but not populated at
            # all for shadow buckets, unfortunately.
            constraints = luci.bucket_constraints(
                service_accounts = [env.coordinator_sa, env.worker_sa],
                pools = all_pools,
            ),
            bindings = [
                # Allow everyone to see builds in public shadow buckets.
                luci.binding(
                    roles = ["role/buildbucket.reader"],
                    groups = "all",
                ),
                # Allow anyone on the Go team to create builds in public shadow buckets.
                # Creating builds is more permissive than triggering them: it allows
                # for arbitrary mutation of the builder definition, whereas triggered
                # builds may only mutate explicitly mutable fields. These shadow buckets
                # are used for testing of public builder configurations and behaviors.
                luci.binding(
                    roles = "role/buildbucket.creator",
                    groups = ["mdb/golang-team"],
                ),

                # Allow our service accounts to create ResultDB invocations in shadow buckets.
                # This permission is necessary to set explicitly according to the shadow bucket
                # documentation.
                luci.binding(
                    roles = "role/resultdb.invocationCreator",
                    users = [env.coordinator_sa, env.worker_sa],
                ),
            ],
        )
    return env

def define_internal_shadow_buckets(env):
    if env.swarming_host != "chrome-swarming.appspot.com":
        fail("only the chrome-swarming instance is known to be intended for internal builds, but env.swarming_host is %s" % env.swarming_host)

    all_pools = [env.coordinator_pool, env.worker_pool]
    if env.shared_worker_pool:
        all_pools.append(env.shared_worker_pool)

    for bucket, shadow_bucket in [(env.bucket, env.shadow_bucket), (env.worker_bucket, env.worker_shadow_bucket)]:
        luci.bucket(
            shadows = bucket,
            name = shadow_bucket,
            dynamic = True,
            # Explicitly set bucket constraints for shadow buckets. These are populated
            # for each real bucket implicitly via luci.builder(...), but not populated at
            # all for shadow buckets, unfortunately.
            constraints = luci.bucket_constraints(
                service_accounts = [env.coordinator_sa, env.worker_sa],
                pools = all_pools,
            ),
            bindings = [
                # Allow only the Go release and security teams to see builds in internal shadow buckets.
                luci.binding(
                    roles = "role/buildbucket.reader",
                    groups = ["mdb/golang-security-policy", "mdb/golang-release-eng-policy"],
                ),
                # Require on-demand access for creating builds, the same access required to
                # access the machines directly.
                #
                # Note: creating builds is more permissive than triggering them. It allows for
                # arbitrary mutation of the builder definition, whereas triggered builds may
                # only mutate explicitly mutable fields. These shadow buckets are used for testing
                # of internal builder configurations and behaviors.
                luci.binding(
                    roles = "role/buildbucket.creator",
                    groups = ["mdb/golang-luci-bot-access"],
                ),

                # Allow our service accounts to create ResultDB invocations in shadow buckets.
                # This permission is necessary to set explicitly according to the shadow bucket
                # documentation.
                luci.binding(
                    roles = "role/resultdb.invocationCreator",
                    users = [env.coordinator_sa, env.worker_sa],
                ),
            ],
        )
    return env

# Create shadow buckets, for mutating and testing out changes to builds.
define_public_shadow_buckets(PUBLIC_TRY_ENV)
define_public_shadow_buckets(PUBLIC_CI_ENV)
define_internal_shadow_buckets(SECURITY_TRY_ENV)

# PT is Project Type, a classification of a project.
PT = struct(
    CORE = "core",  # The Go project or something that it depends on. Needs to be tested everywhere.
    LIBRARY = "library",  # A user-facing library. Needs to be tested on a representative set of platforms.
    TOOL = "tool",  # A developer tool. Typically only run on mainstream platforms such as Linux, MacOS, and Windows.
    SPECIAL = "special",  # None of the above; something that needs a handcrafted set.
)

# PRESUBMIT is a three-state enum describing the possible configurations for presubmit.
PRESUBMIT = struct(
    DISABLED = "disabled for presubmit",
    OPTIONAL = "optional for presubmit",
    ENABLED = "enabled for presubmit",
)

# PROJECTS lists the go.googlesource.com/<project> projects we build and test for.
PROJECTS = {
    "go": PT.CORE,
    "arch": PT.CORE,
    "benchmarks": PT.LIBRARY,
    "build": PT.TOOL,
    "crypto": PT.CORE,
    "debug": PT.LIBRARY,
    "dl": PT.CORE,
    "example": PT.TOOL,
    "exp": PT.SPECIAL,
    "image": PT.LIBRARY,
    "mobile": PT.SPECIAL,
    "mod": PT.CORE,
    "net": PT.CORE,
    "oauth2": PT.LIBRARY,
    "open2opaque": PT.SPECIAL,
    "oscar": PT.TOOL,
    "perf": PT.TOOL,
    "pkgsite": PT.TOOL,
    "pkgsite-metrics": PT.TOOL,
    "playground": PT.TOOL,
    "protobuf": PT.SPECIAL,
    "review": PT.TOOL,
    "scratch": PT.TOOL,
    "sync": PT.CORE,
    "sys": PT.CORE,
    "telemetry": PT.CORE,
    "term": PT.CORE,
    "text": PT.CORE,
    "time": PT.LIBRARY,
    "tools": PT.LIBRARY,
    "vuln": PT.TOOL,
    "vulndb": PT.TOOL,
    "website": PT.TOOL,
    "xerrors": PT.TOOL,
    "vscode-go": PT.SPECIAL,
}

# projects_of_type returns projects of the given types.
def projects_of_type(types):
    return [
        project
        for project in PROJECTS
        if PROJECTS[project] in types
    ]

# make_run_mod returns a run_mod that adds the given properties and
# environment variables.
#
# enabled is an optional function that returns four values that are
# used to affect the builder that the run_mod is being added to:
# - exists - false means the builder should not be created at all
# - presubmit - false means the builder should not run in presubmit by default
# - postsubmit - false means the builder should not run in postsubmit
# - presubmit location filters - any cq.location_filter to apply to presubmit
# If enabled is not provided, the builder defaults are used as is for the run_mod.
#
# known_issues_by_project is an optional map of known issues by project name. It can be
# used to add a project-specific known issue; no support for combining known issues yet.
def make_run_mod(add_props = {}, add_env = {}, enabled = None, known_issues_by_project = None, test_timeout_scale = 1):
    def apply_mod(props, project):
        props.update(add_props)

        # Compose timeout scaling factors by multiplying them.
        if test_timeout_scale != 1:
            if "test_timeout_scale" not in props:
                # Having no test_timeout_scale means that it starts at 1.
                props["test_timeout_scale"] = test_timeout_scale
            else:
                props["test_timeout_scale"] *= test_timeout_scale

        # Update any environment variables.
        e = dict(add_env)
        if "GODEBUG" in e and "GODEBUG" in props["env"]:
            # The environment variable GODEBUG holds a comma-separated list
            # of key=value pairs. Merge them. See go.dev/doc/godebug.
            e["GODEBUG"] = props["env"]["GODEBUG"] + "," + e["GODEBUG"]
        if "GOEXPERIMENT" in e and "GOEXPERIMENT" in props["env"]:
            # The environment variable GOEXPERIMENT holds a comma-separated list
            # of experiment names. Merge them. See https://pkg.go.dev/internal/goexperiment.
            e["GOEXPERIMENT"] = props["env"]["GOEXPERIMENT"] + "," + e["GOEXPERIMENT"]
        props["env"].update(e)

        # Add project-specific known issues.
        if known_issues_by_project and project in known_issues_by_project:
            if "known_issue" in props:
                fail("builder already has a known_issue; no support for combining known issues yet")
            props["known_issue"] = known_issues_by_project[project]

    if enabled == None:
        enabled = lambda port, project, go_branch_short: (True, True, True, [])
    return struct(
        enabled = enabled,
        apply = apply_mod,
    )

# enable only if project matches one of the provided projects and certain source
# locations are modified by the CL, or always for the release branches of the go project.
# projects is a dict mapping a project name to filters.
def define_for_presubmit_only_for_projs_or_on_release_branches(projects):
    def f(port, project, go_branch_short):
        filters = []
        if project == "go":
            presubmit = project in projects or go_branch_short != "gotip"
            if project in projects and go_branch_short == "gotip":
                filters = projects[project]
        else:
            presubmit = project in projects
            if presubmit:
                filters = projects[project]
        return (True, presubmit, True, filters)

    return f

# enable only if port_of(builder_type) matches one of the provided ports, or
# for the release branches in the Go project.
def define_for_presubmit_only_for_ports_or_on_release_branches(ports):
    def f(port, project, go_branch_short):
        presubmit = port in ports or (project == "go" and go_branch_short != "gotip")
        return (True, presubmit, True, [])

    return f

# define the builder only for the go project at versions after x, useful for
# non-default build modes that were created at x.
def define_for_go_starting_at(x, presubmit = True, postsubmit = True):
    def f(port, project, go_branch_short):
        run = project == "go" and (go_branch_short == "gotip" or go_branch_short >= x)
        return (run, run and presubmit, run and postsubmit, [])

    return f

# define the builder only for the go project at versions in [first, last]
# (inclusive), useful for non-default build modes that were created at first
# and removed at last+1. tip is never included.
def define_for_go_range(first, last, presubmit = True, postsubmit = True):
    def f(port, project, go_branch_short):
        run = project == "go" and go_branch_short >= first and go_branch_short <= last
        return (run, run and presubmit, run and postsubmit, [])

    return f

# define the builder only for postsubmit for the specified projects.
#
# Note: it will still be defined for optional inclusion in presubmit.
def define_for_postsubmit(projects, go_branches = GO_BRANCHES.keys()):
    def f(port, project, go_branch_short):
        run = project in projects and go_branch_short in go_branches
        return (run, False, run, [])

    return f

# define the builder only for postsubmit of the go project, or for presubmit
# of the go project if a particular location is touched.
def define_for_go_postsubmit_or_presubmit_with_filters(filters):
    def f(port, project, go_branch_short):
        run = project == "go"
        return (run, run, run, filters)

    return f

# define the builder only for postsubmit of the tip branch of the go project,
# or for presubmit of the go project if a particular location is touched.
def define_for_gotip_postsubmit_or_presubmit_with_filters(filters):
    def f(port, project, go_branch_short):
        run = project == "go" and go_branch_short == "gotip"
        return (run, run, run, filters)

    return f

# define the builder as existing for the go project, so it's includable in presubmit,
# but don't run it anywhere by default.
def define_for_optional_presubmit_only(projects):
    def f(port, project, go_branch_short):
        exists = project in projects
        return (exists, False, False, [])

    return f

def define_for_optional_presubmit_only_ending_at(projects, x):
    def f(port, project, go_branch_short):
        exists = project in projects and (go_branch_short != "gotip" and go_branch_short <= x)
        return (exists, False, False, [])

    return f

# define the builder for all but listed projects.
def define_for_projects_except(projects):
    def f(port, project, go_branch_short):
        run = project not in projects
        return (run, run, run, [])

    return f

# define_for_issue68798 is a custom policy for go.dev/issue/68798.
# It shouldn't be needed beyond Go 1.24.
def define_for_issue68798():
    def f(port, project, go_branch_short):
        # Starting with Go 1.23, gotypesalias=1 is the default, so
        # a builder that sets it explicitly in the environment is expected to be a no-op.
        # Run it anyway to confirm that's the case for reasons motivated in go.dev/issue/68798.
        exists, presubmit, postsubmit = False, True, True
        if project in ["go", "tools"]:
            exists = go_branch_short != "gotip" and go_branch_short <= "go1.24"
        return (exists, presubmit, postsubmit, [])

    return f

# define_for_issue69121 is a custom policy for go.dev/issue/69121.
# It shouldn't be needed beyond Go 1.25.
# TODO: delete when Go 1.24 is end of support.
def define_for_issue69121():
    def f(port, project, go_branch_short):
        exists, presubmit, postsubmit = False, True, True
        if project in ["go", "tools"]:
            exists = go_branch_short == "go1.24"
        return (exists, presubmit, postsubmit, [])

    return f

# RUN_MODS is a list of valid run-time modifications to the way we
# build and test our various projects.
RUN_MODS = dict(
    # Build and test with the aliastypeparams GOEXPERIMENT, which enables
    # aliases with type parameters and the V2 unified IR exportdata format.
    aliastypeparams = make_run_mod(
        add_env = {"GODEBUG": "gotypesalias=1", "GOEXPERIMENT": "aliastypeparams"},
        enabled = define_for_issue69121(),
    ),

    # Build and test with AddressSanitizer enabled.
    asan = make_run_mod(
        add_props = {"asan_mode": True},
        test_timeout_scale = 2,
        enabled = define_for_go_starting_at("go1.24", presubmit = False),
    ),

    # Build and test with the boringcrypto GOEXPERIMENT.
    boringcrypto = make_run_mod(
        add_env = {"GOEXPERIMENT": "boringcrypto"},
    ),

    # Build and test clang 15 as the C toolchain.
    clang15 = make_run_mod(
        add_props = {"clang_version": "15.0.6"},
        enabled = define_for_postsubmit(["go"]),
    ),

    # Build and test with GOAMD64=v3, which makes the compiler assume certain amd64 CPU
    # features are always available.
    goamd64v3 = make_run_mod(
        add_env = {"GOAMD64": "v3"},
        enabled = define_for_postsubmit(["go"]),
    ),

    # Run a larger set of tests.
    longtest = make_run_mod(
        add_props = {"long_test": True},
        test_timeout_scale = 5,
        enabled = define_for_presubmit_only_for_projs_or_on_release_branches({
            "benchmarks": [
                # Enable longtest builders on x/benchmarks if files related to sweet are modified,
                # so that the sweet end-to-end test is run.
                "sweet/.+[.]go",
            ],
            "build": [],
            "go": [
                # Enable longtest builders on go against tip if files related to vendored code are modified.
                "src(|/.+)/go[.](mod|sum)",
                "src(|/.+)/vendor/.+",
                "src/.+_bundle.go",
                # Enable longtest builders on go against tip if files in the crypto/tls tree are modified,
                # so that the BoGo test suite is run.
                "src/crypto/tls/.+",
                # Enable longtest builders on go against tip if files in the cmd/go tree are modified,
                # so the many cmd/go script tests that are skipped on short are run.
                "src/cmd/go/.+",
            ],
            "protobuf": [],
        }),
    ),

    # The misccompile mod indicates that the builder should act as a "misc-compile" builder,
    # that is to cross-compile all non-first-class ports to quickly flag portability issues.
    misccompile = make_run_mod(
        add_props = {"compile_only": True, "misc_ports": True},
        enabled = define_for_projects_except(["oscar"]),
    ),

    # Build and test with MemorySanitizer enabled.
    msan = make_run_mod(
        add_props = {"msan_mode": True},
        test_timeout_scale = 2,
        enabled = define_for_go_starting_at("go1.24", presubmit = False),
    ),

    # Build and test with the newinliner GOEXPERIMENT.
    newinliner = make_run_mod(
        add_env = {"GOEXPERIMENT": "newinliner"},
        enabled = define_for_go_starting_at("go1.22"),
    ),

    # Build and test with cgo disabled.
    nocgo = make_run_mod(
        add_env = {"CGO_ENABLED": "0"},
        enabled = define_for_postsubmit(projects_of_type([PT.CORE, PT.LIBRARY])),
    ),

    # Build and test with GOEXPERIMENT=nogreenteagc.
    nogreenteagc = make_run_mod(
        add_env = {"GOEXPERIMENT": "nogreenteagc"},
        enabled = define_for_postsubmit(["go"], ["gotip"]),
    ),

    # Build and test with optimizations disabled.
    noopt = make_run_mod(
        add_env = {"GO_GCFLAGS": "-N -l"},
        enabled = define_for_postsubmit(["go"]),
    ),

    # Build and test with the swissmap GOEXPERIMENT disabled.
    #
    # GOEXPERIMENT=swissmap was deleted in Go 1.26. This can be deleted when Go
    # 1.25 is no longer supported.
    noswissmap = make_run_mod(
        add_env = {"GOEXPERIMENT": "noswissmap"},
        enabled = define_for_go_range("go1.24", "go1.25", presubmit = False),
    ),

    # Run performance tests with PGO against the oldest stable Go release.
    #
    # Note: This run_mod is incompatible with most other run_mods. Generally, build-time
    # environment variables will apply, but others like compile_only, race, and longtest
    # will have no effect.
    #
    # This should eventually be the default, because it produces a strict superset of data
    # compared vs. the others performance run-mods. This is just to test and make sure it
    # works.
    perf_pgo_vs_oldest_stable = make_run_mod(
        add_props = {"perf_mode": {"baseline": "refs/heads/" + GO_BRANCHES[SECOND_GO].branch, "pgo": True}},
        enabled = define_for_optional_presubmit_only(["go"]),
    ),

    # Run performance tests against the gopls-release-branch.0.11 branch as a baseline. Only
    # makes sense for the x/tools repository. Excludes gotip as a branch for testing since the
    # same toolchain tool would be chosen as the baseline by golangbuild for gotip and the latest
    # release branch, making it redundant (and also misleading).
    #
    # Note: This run_mod is incompatible with most other run_mods. Generally, build-time
    # environment variables will apply, but others like compile_only, race, and longtest
    # will have no effect.
    perf_vs_gopls_0_11 = make_run_mod(
        add_props = {"perf_mode": {"baseline": "refs/heads/gopls-release-branch.0.11"}},
        enabled = define_for_postsubmit(["tools"], go_branches = [LATEST_GO]),
    ),

    # Run performance tests against the oldest stable Go release.
    #
    # Note: This run_mod is incompatible with most other run_mods. Generally, build-time
    # environment variables will apply, but others like compile_only, race, and longtest
    # will have no effect.
    perf_vs_oldest_stable = make_run_mod(
        add_props = {"perf_mode": {"baseline": "refs/heads/" + GO_BRANCHES[SECOND_GO].branch}},
        enabled = define_for_optional_presubmit_only(["go"]),
    ),

    # Run performance tests against the source's parent commit.
    #
    # Note: This run_mod is incompatible with most other run_mods. Generally, build-time
    # environment variables will apply, but others like compile_only, race, and longtest
    # will have no effect.
    perf_vs_parent = make_run_mod(
        add_props = {"perf_mode": {"baseline": "parent"}},
        enabled = define_for_optional_presubmit_only(["go", "tools"]),
    ),

    # Run performance tests against the latest Go release as a baseline. Only makes sense
    # for the main Go repository.
    #
    # When run against a commit or change on tip, it will pick the latest overall release.
    # When run against a commit or change on a release branch, it will pick the latest
    # release on that release branch.
    #
    # Note: This run_mod is incompatible with most other run_mods. Generally, build-time
    # environment variables will apply, but others like compile_only, race, and longtest
    # will have no effect.
    perf_vs_release = make_run_mod(
        add_props = {"perf_mode": {"baseline": "latest_go_release"}},
        enabled = define_for_postsubmit(["go"]),
    ),

    # Run performance tests against the tip of the main branch for whatever repository
    # we're targeting.
    #
    # Note: This run_mod is incompatible with most other run_mods. Generally, build-time
    # environment variables will apply, but others like compile_only, race, and longtest
    # will have no effect.
    perf_vs_tip = make_run_mod(
        add_props = {"perf_mode": {"baseline": "refs/heads/" + MAIN_BRANCH_NAME}},
        enabled = define_for_optional_presubmit_only(["go", "tools"]),
    ),

    # Build and test with race mode enabled.
    race = make_run_mod(
        add_props = {"race_mode": True},
        test_timeout_scale = 2,
        enabled = define_for_presubmit_only_for_ports_or_on_release_branches(["linux-amd64"]),
    ),

    # Build with a compiler and linker that are built with race mode enabled.
    racecompile = make_run_mod(
        add_props = {"compile_only": True, "compiler_linker_race_mode": True},
        enabled = define_for_postsubmit(["go"]),
    ),

    # Build and test with GOEXPERIMENT=runtimefreegc.
    #
    # This is an experiment for new functionality in 2026.
    # This builder is useful while the experiment is still
    # in progress, but should be removed if nobody else is
    # working on it, or we remove the experiment. We'll
    # re-evaluate the need for this builder mid-year.
    runtimefreegc = make_run_mod(
        add_env = {"GOEXPERIMENT": "runtimefreegc"},
        enabled = define_for_postsubmit(["go"], ["gotip"]),
    ),

    # Build and test with GOEXPERIMENT=sizespecializedmalloc.
    sizespecializedmalloc = make_run_mod(
        add_env = {"GOEXPERIMENT": "sizespecializedmalloc"},
        enabled = define_for_go_postsubmit_or_presubmit_with_filters(["src/runtime/[^/]+"]),
    ),

    # Build and test with GO386=softfloat, which makes the compiler emit non-floating-point
    # CPU instructions to perform floating point operations.
    softfloat = make_run_mod(
        add_env = {"GO386": "softfloat"},
        enabled = define_for_postsubmit(["go"]),
    ),

    # Build with ssacheck mode enabled in the compiler.
    ssacheck = make_run_mod(
        add_props = {"compile_only": True},
        add_env = {"GO_GCFLAGS": "-d=ssa/check/on"},
        enabled = define_for_go_postsubmit_or_presubmit_with_filters(["src/cmd/compile/internal/(ssa|ssagen)/.+"]),
    ),

    # Build and test with the staticlockranking GOEXPERIMENT, which validates the runtime's
    # dynamic lock usage against a static ranking to detect possible deadlocks before they happen.
    staticlockranking = make_run_mod(
        add_env = {"GOEXPERIMENT": "staticlockranking"},
        enabled = define_for_go_postsubmit_or_presubmit_with_filters(["src/runtime/[^/]+"]),
    ),

    # Build and test with go.mod upgraded to the latest version.
    #
    # Enabled only in postsubmit for non-special subrepos.
    tiplang = make_run_mod(
        add_props = {"upgrade_go_mod_lang": True},
        enabled = define_for_postsubmit([
            proj
            for proj, typ in PROJECTS.items()
            if proj != "go" and typ != PT.SPECIAL
        ], go_branches = ["gotip"]),
    ),

    # Build and test with the gotypesalias GODEBUG, which enables
    # explicit representation of type aliases.
    typesalias = make_run_mod(
        add_env = {"GODEBUG": "gotypesalias=1"},
        enabled = define_for_issue68798(),
    ),
)

# EXTRA_DEPENDENCIES specifies custom additional dependencies
# to append when applies(project, port, run_mods) matches.
EXTRA_DEPENDENCIES = [
    # The protobuf repo needs extra dependencies for its integration test.
    # See its integration_test.go file and go.dev/issue/64066.
    struct(
        applies = lambda project, port, run_mods: project == "protobuf" and port == "linux-amd64" and "longtest" in run_mods,
        test_deps = """@Subdir bin
golang/third_party/protoc_with_conformance/${platform} version:v33.3
""",
    ),
]

def go_cq_group(project, go_branch_short):
    """go_cq_group returns the CQ group name and watch for project and
    go_branch_short."""

    # The CQ group "{project}_repo_{go-branch}" is configured to watch
    # applicable branches in project and test with the Go version that
    # corresponds to go-branch.
    #
    # LUCI's CQ group names must match "^[a-zA-Z][a-zA-Z0-9_-]{0,39}$".
    # Each branch must be watched by no more than one matched CQ group.
    cq_group_name = ("%s_repo_%s" % (project, go_branch_short)).replace(".", "-")

    if project == "go":
        # Main Go repo trybot branch coverage.
        if go_branch_short == "gotip":
            refs, refs_exclude = ["^refs/heads/.+$"], ["^refs/heads/release-branch\\..+$"]
        else:
            refs, refs_exclude = ["^refs/heads/release-branch\\.%s$" % go_branch_short.replace(".", "\\.")], None
    else:
        # golang.org/x repo trybot branch coverage.
        # See go.dev/issue/46154.
        if go_branch_short == "gotip":
            refs, refs_exclude = ["^refs/heads/.+$"], ["^refs/heads/internal-branch\\..+$"]
        else:
            refs, refs_exclude = ["^refs/heads/internal-branch\\.%s-.+$" % go_branch_short.replace(".", "\\.")], None

    return struct(
        name = cq_group_name,
        watch = cq.refset(
            repo = "https://go.googlesource.com/%s" % project,
            refs = refs,
            refs_exclude = refs_exclude,
        ),
    )

def split_builder_type(builder_type):
    """split_builder_type splits a builder type into its pieces.

    Args:
        builder_type: the builder type.

    Returns:
        The builder type's GOOS, GOARCH, OS version, and run mods.
    """
    parts = builder_type.split("-")
    os, arch = parts[0], parts[1]
    suffix = ""
    if "_" in arch:
        arch, suffix = arch.split("_", 2)

    return os, arch, suffix, parts[2:]

def port_of(builder_type):
    """port_of returns the builder_type stripped of OS version and run mods.

    Args:
        builder_type: the builder type.

    Returns:
        The builder type's GOOS and GOARCH, without OS version and run mods.
    """
    os, arch, _, _ = split_builder_type(builder_type)
    return "%s-%s" % (os, arch)

def host_of(builder_type):
    """host_of returns the host builder type for builder_type.

    For example, linux-amd64 is the host for js-wasm as of writing.

    Args:
        builder_type: the builder type.

    Returns:
        The host builder type.
    """
    goos, goarch, suffix, _ = split_builder_type(builder_type)

    # We run various builds on a Linux host.
    if goarch == "wasm":
        return "linux-amd64"

    port = "%s-%s" % (goos, goarch)
    if suffix != "":
        return port + "_" + suffix
    return port

def dimensions_of(host_type):
    """dimensions_of returns the bot dimensions for a host type."""
    goos, goarch, suffix, _ = split_builder_type(host_type)

    # We run some 386 ports on amd64 machines.
    if goarch == "386" and goos in ("linux", "windows"):
        goarch = "amd64"

    host = "%s-%s" % (goos, goarch)

    os, cpu = None, None

    # Narrow down the dimensions using the suffix.

    # Set the default suffix, if we don't have one and one exists.
    if suffix == "" and host in DEFAULT_HOST_SUFFIX:
        suffix = DEFAULT_HOST_SUFFIX[host]

    # Fail if we don't have a suffix and this isn't a low capacity host.
    #
    # It's really important that we specify an OS version so that
    # robocrop can correctly and precisely identify task queue length
    # for different types of machines. This *must* line up with the
    # expected_dimensions field for the botset in the internal config:
    # //starlark/common/envs/golang.star
    #
    # Low capacity hosts don't require a suffix because the OS versions are
    # manually managed and we don't always control them, so we want to be
    # as general as possible to avoid downtime.
    #
    # Note: it's a little odd that we don't rely on a low_capacity_hosts
    # passed down to us from an environment, but that's because we care about
    # the broadest possible definition of "low capacity" here for OS version
    # specification.
    if suffix == "" and host not in LOW_CAPACITY_HOSTS:
        fail("failed to find required OS version for host %s" % host)

    if suffix != "":
        if goos == "linux" and "debian" in suffix:
            # linux-amd64_debian11  -> Debian-11
            # linux-amd64_debiansid -> Debian-13
            os = suffix.replace("debian", "Debian-").replace("sid", "13")
        elif goos == "linux" and suffix in ["avx512", "c2s16", "c3h88", "c4as16", "c4ah72"]:
            # Machines with special architecture and performance test machines.
            os = "Debian-12"
        elif goos == "linux" and goarch in ["ppc64", "ppc64le"]:
            cpu = goarch + "-64-" + suffix.replace("power", "POWER")
        elif goos == "linux" and goarch == "amd64" and suffix == "docker":
            os = "Ubuntu-22"
        elif goos == "darwin":
            # darwin-amd64_12.6 -> Mac-12.6
            os = "Mac-" + suffix
        elif goos == "windows":
            os = "Windows-" + suffix
        elif goos == "openbsd":
            os = "openbsd-" + suffix
        elif goos == "freebsd":
            os = "freebsd-" + suffix
        else:
            fail("unhandled suffix %s for host type %s" % (suffix, host_type))

    # Request a specific GCE machine type for certain platforms.
    #
    # This is very, very important for auto-scaling, it this needs to
    # match the "expected_dimensions" field that botsets have in the
    # internal configuration. Take care when updating this in general,
    # it probably also requires an update to the internal configuration.
    machine_type = None
    if goos == "linux":
        if goarch == "amd64":
            if suffix == "c2s16":
                # Performance test machines.
                machine_type = "c2-standard-16"
            elif suffix == "c3h88":
                machine_type = "c3-highcpu-88"
            elif suffix == "avx512":
                machine_type = "c3-standard-8"
            else:
                machine_type = "n1-standard-16"
        elif goarch == "arm64":
            if suffix == "c4as16":
                machine_type = "c4a-standard-16"
            elif suffix == "c4ah72":
                machine_type = "c4a-highcpu-72"
            else:
                machine_type = "t2a-standard-8"

    # cipd_platform is almost "$GOHOSTOS-$GOHOSTARCH", with some deviations.
    if goos == "darwin":
        cipd_platform = "mac-" + goarch  # GOHOSTOS=darwin is written as "mac".
    elif goarch == "arm":
        suffix = "v7l" if goos == "netbsd" else "v6l"
        cipd_platform = goos + "-arm" + suffix  # GOHOSTARCH=arm is written as "arm{suffix}".
    else:
        cipd_platform = host

    if goos == "plan9":  # TODO(go.dev/issue/62025): Simplify builder definition.
        return {
            "cipd_platform": "linux-amd64",
            "target_goos": goos,
            "target_goarch": goarch,
        }

    dims = {"cipd_platform": cipd_platform}
    if os != None:
        dims["os"] = os
    if cpu != None:
        dims["cpu"] = cpu
    if machine_type != None:
        dims["machine_type"] = machine_type

    # machines with docker installed have a special dimension.
    if suffix == "docker":
        dims["docker_installed"] = "true"
    return dims

def is_capacity_constrained(low_capacity_hosts, host_type):
    dims = dimensions_of(host_type)
    return any([dimensions_of(x) == dims for x in low_capacity_hosts])

def is_fully_supported(dims):
    """Reports whether dims identifies a platform fully supported by LUCI.

    Some packages, notably Python, Go, and Git aren't built into CIPD for
    all platforms or at all versions we need. We work around their absence
    on unsupported platforms.

    Args:
        dims: The dimensions of a task/bot.
    """

    if "target_goos" in dims and dims["target_goos"] == "plan9":  # TODO(go.dev/issue/62025): Simplify builder definition.
        return False

    supported_cipd_platforms = [
        "%s-%s" % (os, arch)
        for os in ["linux", "mac", "windows"]
        for arch in ["armv6l", "amd64", "arm64"]
    ]
    return dims["cipd_platform"] in supported_cipd_platforms

# builder_name produces the final builder name.
def builder_name(project, go_branch_short, builder_type):
    """Derives the name for a certain builder.

    Args:
        project: A go project defined in `PROJECTS`.
        go_branch_short: A go repository branch name defined in `GO_BRANCHES`.
        builder_type: A name defined in `BUILDER_TYPES`.

    Returns:
        The full name for the builder with the given specs.
    """

    if project == "go":
        # Omit the project name for the main Go repository.
        # The branch short name already has a "go" prefix so
        # it's clear what the builder is building and testing.
        return "%s-%s" % (go_branch_short, builder_type)

    elif project in ["dl", "protobuf", "open2opaque"]:
        # A special, non-golang.org/x/* repository.
        # Put "z" at the beginning to sort this at the bottom of the builder list.
        return "z_%s-%s-%s" % (project, go_branch_short, builder_type)

    # Add an x_ prefix to the project to help make it clear that
    # we're testing a golang.org/x/* repository. These repositories
    # do not have an "internal" counterpart.
    return "x_%s-%s-%s" % (project, go_branch_short, builder_type)

# project_title produces a short title for the given project.
def project_title(project):
    if project == "go":
        fail("project_title doesn't have support for the 'go' project")
    elif project == "dl":
        return "golang.org/dl"
    elif project in ["protobuf", "open2opaque"]:
        return "google.golang.org/%s" % project
    else:
        # A golang.org/x/* repository. Since these are very common,
        # the 'golang.org/' prefix is left out for brevity.
        return "x/" + project

# make_console_gen returns a console generator struct whose gen field
# is a function that generates a console.
def make_console_gen(project, go_branch_short, builders, by_go_commit = False, known_issue = False, tier = 1):
    repo = project
    if project == "go":
        name = "%s-%s" % (project, go_branch_short)
        title = go_branch_short
        ref = "refs/heads/" + GO_BRANCHES[go_branch_short].branch
    else:
        if project_title(project).startswith("x/"):
            name = "x-%s-%s" % (project, go_branch_short)
        else:
            name = "z-%s-%s" % (project, go_branch_short)
        title = project_title(project) + " (" + go_branch_short + ")"
        ref = "refs/heads/" + MAIN_BRANCH_NAME
        if by_go_commit:
            name += "-by-go"
            title += " by go commit"
            ref = "refs/heads/" + GO_BRANCHES[go_branch_short].branch
            repo = "go"
    header = None
    if known_issue:
        title += " (known issue)"
        name += "-known_issue"
        links = []
        for builder_name, b in builders.items():
            builder_name = builder_name.split("/")[1]  # Remove the bucket.
            links.append({
                "text": "%s: go.dev/issue/%d" % (builder_name, b.known_issue),
                "url": "https://go.dev/issue/%d" % b.known_issue,
                "alt": "Known issue link for %s" % builder_name,
            })
        header = {"links": [{"name": "Known issues", "links": links}]}

    def gen():
        luci.console_view(
            name = name,
            repo = "https://go.googlesource.com/%s" % repo,
            title = title,
            refs = [ref],
            entries = [
                luci.console_view_entry(
                    builder = name,
                    category = display_for_builder_type(b.builder_type)[0],
                    short_name = display_for_builder_type(b.builder_type)[1],
                )
                for name, b in builders.items()
            ],
            header = header,
        )

    return struct(
        name = name,
        title = title,
        tier = tier,
        gen = gen,
    )

# Enum values for golangbuild's "mode" property.
GOLANGBUILD_MODES = {
    "ALL": 0,
    "COORDINATOR": 1,
    "BUILD": 2,
    "TEST": 3,
    "PERF": 4,
}

def define_builder(env, project, go_branch_short, builder_type):
    """Creates a builder definition.

    Args:
        env: the environment the builder runs in.
        project: A go project defined in `PROJECTS`.
        go_branch_short: A go repository branch name defined in `GO_BRANCHES` or `TOOLS_GO_BRANCHES`.
        builder_type: A name defined in `BUILDER_TYPES`.

    Returns:
        The full name including a bucket prefix.
        A list of the builders this builder will trigger (by full name).
        A known issue number that is set for the builder, or 0 if none.
    """

    os, arch, suffix, run_mods = split_builder_type(builder_type)
    host_type = host_of(builder_type)
    hostos, hostarch, _, _ = split_builder_type(host_type)

    if os == "plan9":  # TODO(go.dev/issue/62025): Simplify builder definition.
        hostos, hostarch = "linux", "amd64"

    # Construct the basic properties that will apply to all builders for
    # this combination.
    known_go_branches = dict(GO_BRANCHES)
    known_go_branches.update(TOOLS_GO_BRANCHES)
    base_props = {
        "project": project,
        # NOTE: LUCI will pass in the commit information. This is
        # extra information that's only necessary for x/ repos.
        # However, we pass it to all builds for consistency and
        # convenience.
        "go_branch": known_go_branches[go_branch_short].branch,
        "bootstrap_version": known_go_branches[go_branch_short].bootstrap,
        "host": {"goos": hostos, "goarch": hostarch},
        "target": {"goos": os, "goarch": arch},
        "env": {},
        "is_google": not is_capacity_constrained(LOW_CAPACITY_HOSTS, host_type) or is_capacity_constrained(GOOGLE_LOW_CAPACITY_HOSTS, host_type),
    }
    if host_timeout_scale(host_type) != 1:
        base_props["test_timeout_scale"] = host_timeout_scale(host_type)
    if builder_type in env.known_issue_builder_types:
        base_props["known_issue"] = env.known_issue_builder_types[builder_type].issue_number
    for d in EXTRA_DEPENDENCIES:
        if not d.applies(project, port_of(builder_type), run_mods):
            continue
        if d.test_deps:
            base_props["tools_extra_test"] = d.test_deps

    # We run GOARCH=wasm builds on linux/amd64 with GOOS/GOARCH set,
    # and the applicable Wasm runtime provided as a CIPD dependency.
    #
    # The availability of a given version in CIPD can be checked with:
    #   cipd search infra/3pp/tools/{wasm_runtime}/linux-amd64 -tag=version:{version}
    # Where wasm_runtime is one of nodejs, wasmtime, wazero.
    if arch == "wasm":
        if os == "js":
            if suffix != "":
                fail("unknown GOOS=js builder suffix: %s" % suffix)
            base_props["node_version"] = "2@18.8.0"
        elif os == "wasip1":
            if suffix == "wasmtime":
                base_props["env"]["GOWASIRUNTIME"] = "wasmtime"
                base_props["wasmtime_version"] = "2@14.0.4"
            elif suffix == "wazero":
                base_props["env"]["GOWASIRUNTIME"] = "wazero"
                base_props["wazero_version"] = "3@1.8.1"
            else:
                fail("unknown GOOS=wasip1 builder suffix: %s" % suffix)

    # Set GOPPC64 explicitly when it's specified in the suffix.
    if arch in ["ppc64", "ppc64le"] and "power" in suffix:
        base_props["env"]["GOPPC64"] = suffix

    # TODO(go.dev/issue/65241): Start by mirroring old infra's linux-arm env,
    # which came from CL 390395 and that came from CL 35501 for issue 18748.
    # Since then the default for GOARM has changed¹, so these should be unset
    # to make the builder more representative of a common default environment.
    # ¹ https://go.dev/doc/go1.22#arm
    if builder_type == "linux-arm":
        base_props["env"]["GOARM"] = "6"

    # TODO(go.dev/issue/70213): Throttle back the load average.
    if builder_type == "openbsd-ppc64":
        base_props["env"]["GOMAXPROCS"] = "8"

    # Construct the basic dimensions for the build/test running part of the build.
    #
    # Note that these should generally live in the worker pools.
    base_dims = dimensions_of(host_type)
    base_dims["pool"] = env.worker_pool
    if is_capacity_constrained(env.low_capacity_hosts, host_type) and env.shared_worker_pool != "":
        # Scarce resources live in the shared-workers pool when it is available.
        base_dims["pool"] = env.shared_worker_pool

    # On less-supported platforms, we may not have bootstraps before 1.21
    # started cross-compiling everything.
    if not is_fully_supported(base_dims):
        if base_props["bootstrap_version"] < "1.21.0":
            base_props["bootstrap_version"] = "1.21.0"

    # Handle bootstrap for new ports.
    if os == "openbsd" and arch == "ppc64":  # See go.dev/doc/go1.22#openbsd.
        if base_props["bootstrap_version"] < "1.22.0":
            base_props["bootstrap_version"] = "1.22.0"
    if os == "openbsd" and arch == "riscv64":  # See go.dev/issue/55999, https://go.dev/doc/go1.23#openbsd.
        if base_props["bootstrap_version"] < "1.23.0":
            base_props["bootstrap_version"] = "1.23-devel-20240524222355-377646589d5f"

    if os == "darwin":
        # See available versions with: cipd instances -limit 0 infra_internal/ios/xcode/mac | less
        xcode_versions = {
            11: "12b45b",  # released Nov 2020, macOS 11 released Nov 2020
            12: "13c100",  # released Dec 2021, macOS 12 released Oct 2021
            13: "15c500b",  # released Jan 2024, macOS 13.5 released Jul 2023
            14: "15e204a",  # released Mar 2023, macOS 14 released Sep 2023
            15: "16e140",  # released Mar 2025, macOS 15.2 released Dec 2024
        }
        base_props["xcode_version"] = xcode_versions[int(dimensions_of(host_type)["os"].split(".")[0].replace("Mac-", ""))]

    # Turn on the no-network check.
    if builder_type in NO_NETWORK_BUILDERS:
        base_props["no_network"] = True

    # Set GOEXPERIMENT=synctest in x/net for go1.24. (Go 1.25+ includes it by default.)
    #
    # N.B. This must come before applying run mods, which may add more
    # GOEXPERIMENTs.
    if project == "net" and go_branch_short == "go1.24":
        base_props["env"]["GOEXPERIMENT"] = "synctest"

    # Increase the timeout for vscode-go tests to accommodate significant setup
    # overhead, including building Docker images and initializing the VS Code
    # extension host.
    if project == "vscode-go":
        base_props["test_timeout_scale"] = 2

    for mod in run_mods:
        if not mod in RUN_MODS:
            fail("unknown run mod: %s" % mod)
        RUN_MODS[mod].apply(base_props, project)

    # Named cache for git clones.
    base_props["git_cache"] = "git"

    # Named cache for cipd tools root.
    base_props["tools_cache"] = "tools"

    caches = [
        swarming.cache(base_props["git_cache"]),
        swarming.cache(base_props["tools_cache"]),
    ]

    # Determine which experiments to apply.
    experiments = {
        "golang.shard_by_weight": 100,
    }

    # Construct the executable reference.
    executable = luci.executable(
        name = "golangbuild",
        cipd_package = "infra/experimental/golangbuild/${platform}",
        cipd_version = "latest",
        cmd = ["golangbuild"],
    )

    # Determine how long we should wait for a machine to become available before the
    # build expires. Only applies if there's at least one machine. Builds fail immediately
    # if there are no machines matching the dimensions, unless the builder's wait_for_capacity
    # field is set.
    #
    # For builders targeting platforms that we have plenty of resources for, wait up to 6 hours.
    # Robocrop should scale much sooner than that to fill demand, but if we end up with more demand
    # than our cap we'll have 6 hours for that demand to be filled before we report it as a problem.
    # We do not wait for capacity on these builders so we can find out immediately out all our
    # capacity is drained. It is usually indicative of a bigger issue, like an outage.
    #
    # For builders that are capacity constrained, wait several times longer. Note
    # that the time we can wait here is not usually unbounded, since according to triggering_policy there
    # can only be at most 1 outstanding request per repo+gobranch combination in postsubmit.
    # Therefore, this constant should generally not need to be bumped up. The only case where it
    # might need it is a high rate of presubmit builds including such builders, but the fact
    # that builds expire is good: it acts as a backpressure mechanism. We wait for capacity on
    # these builders because they frequently go down for maintenance or just because they're flaky.
    capacity_constrained = is_capacity_constrained(env.low_capacity_hosts, host_type)
    expiration_timeout = 6 * time.hour
    wait_for_capacity = False
    if capacity_constrained:
        expiration_timeout = time.day
        wait_for_capacity = True

    # Define notifications for the builder.
    notifiers = []

    # Add a notification for machine owners on infra failures.
    if host_type in HOST_NOTIFIERS:
        notifiers.append(HOST_NOTIFIERS[host_type])

    # Create a helper to emit builder definitions, installing common fields from
    # the current context.
    def emit_builder(name, bucket, dimensions, properties, service_account, **kwargs):
        exps = dict(experiments)
        if not is_fully_supported(dimensions):
            exps["luci.best_effort_platform"] = 100
        luci.builder(
            name = name,
            bucket = bucket,
            executable = executable,
            dimensions = dimensions,
            properties = properties,
            service_account = service_account,
            swarming_host = env.swarming_host,
            resultdb_settings = resultdb.settings(
                enable = True,
            ),
            caches = caches,
            experiments = exps,
            expiration_timeout = expiration_timeout,
            wait_for_capacity = wait_for_capacity,
            priority = env.priority,
            notifies = notifiers,
            **kwargs
        )

    name = builder_name(project, go_branch_short, builder_type)

    # Determine if we should be sharding tests, and how many shards.
    compile_only = "compile_only" in base_props
    misccompile = "misccompile" in run_mods
    longtest = "longtest" in run_mods
    perfmode = "perf_mode" in base_props
    if compile_only:
        if misccompile:
            if project == "go":
                test_shards = 12
            else:
                test_shards = 3
        else:
            # Compile-only builders for a single platform should have just one shard, otherwise
            # each shard will do repeat work.
            test_shards = 1
    elif perfmode:
        test_shards = 1
    elif project == "go" and not capacity_constrained:
        if longtest:
            test_shards = 8
        else:
            test_shards = 4
    else:
        test_shards = 1

    # Emit the builder definitions.
    #
    # For each builder type we have two possible options for emitting a builder. Either it's going to be
    # a sharded builder, which actually consists of 3 builders for coordination, build, and test, or a single
    # builder all-mode which does all 3 on the same machine.
    #
    # The sharded builder is mainly used for sharding tests, but it is also useful for sharding misccompile
    # builds. Even if we have only 1 shard, a sharded builder is still useful because only coordinator builders
    # are allowed to spawn other builds, and so these coordination builders may spawn downstream builds.
    # For example, builds of Go cache the toolchain. Triggering subrepo builds once the cache is filled is ideal,
    # because the subrepo builds can use the cached toolchain. To actually run sharded builds we can't be
    # capacity constrained, and we must only run for the main Go repo, because that's the only case we support
    # for test sharding in golangbuild currently.
    #
    # The all-mode builder is used for everything else, but we must only select it if test_shards == 1.
    downstream_builders = []
    if perfmode:
        define_perfmode_builder(env, name, builder_type, base_props, base_dims, emit_builder)
    elif test_shards > 1 or (project == "go" and not capacity_constrained):
        downstream_builders = define_sharded_builder(env, project, name, test_shards, go_branch_short, builder_type, run_mods, base_props, base_dims, emit_builder)
    elif test_shards == 1:
        define_allmode_builder(env, name, builder_type, base_props, base_dims, emit_builder)
    else:
        fail("unhandled builder definition")

    known_issue = base_props["known_issue"] if "known_issue" in base_props else 0
    return env.bucket + "/" + name, downstream_builders, known_issue

def define_sharded_builder(env, project, name, test_shards, go_branch_short, builder_type, run_mods, base_props, base_dims, emit_builder):
    os, arch, _, _ = split_builder_type(builder_type)

    # Create 3 builders: the main entrypoint/coordinator builder,
    # a builder just to run make.bash, and a builder to run tests.
    #
    # This separation of builders allows for the coordinator builder to have elevated privileges,
    # because it will never run code still in code review. Furthermore, this separation allows
    # for sharding tests, which lets us improve build latency beyond what one machine is able to provide.
    #
    # The main/coordinator builder must be the only one with the ability
    # to schedule new builds.
    coord_name = name
    if project == "go":
        build_name = name + "-build_go"
    else:
        build_name = builder_name("go", go_branch_short, builder_type) + "-build_go"
    test_name = name + "-test_only"

    # The main repo builder also triggers subrepo builders of the same builder type.
    #
    # This is currently only used to trigger CI builders, not trybots, because
    # a builder triggered this way by default will be considered as optional.
    #
    # TODO(mknyszek): This rule will not apply for some ports in the future. Some
    # ports only apply to the main Go repository and are not supported by all subrepos.
    # PROJECTS should probably contain a table of supported ports or something.
    builders_to_trigger = []
    if project == "go" and env.bucket == "ci":
        builders_to_trigger = [
            "%s/%s" % (env.bucket, builder_name(project, go_branch_short, builder_type))
            for project in PROJECTS
            if project != "go" and enabled(env.low_capacity_hosts, project, go_branch_short, builder_type, env.known_issue_builder_types)[2]
        ]

    # Coordinator builder.
    #
    # Specify the dimensions directly. Coordinator builds can have very vague dimensions
    # because we don't really care what machines they run on. We still need to specify
    # something for robocrop, but the robocrop config will look for just "Linux."
    coord_dims = {
        "pool": env.coordinator_pool,
        "cipd_platform": "linux-amd64",
    }

    coord_props = dict(base_props)
    coord_props.update({
        "mode": GOLANGBUILD_MODES["COORDINATOR"],
        "coord_mode": {
            "build_builder": "golang/" + env.worker_bucket + "/" + build_name,
            "test_builder": "golang/" + env.worker_bucket + "/" + test_name,
            "num_test_shards": test_shards,
            "builders_to_trigger_after_toolchain_build": ["golang/%s" % name for name in builders_to_trigger],
        },
    })
    emit_builder(
        name = coord_name,
        bucket = env.bucket,
        dimensions = coord_dims,
        properties = coord_props,
        triggering_policy = triggering_policy(env, builder_type),
        service_account = env.coordinator_sa,
    )

    # Build builder.
    if project == "go":
        build_dims = dict(base_dims)
        build_props = dict(base_props)
        build_props.update({
            "mode": GOLANGBUILD_MODES["BUILD"],
            "build_mode": {},
            # For sharded x repo builders, go_commit will be overwritten by the coordinator builder.
            "go_commit": "",
        })
        emit_builder(
            name = build_name,
            bucket = env.worker_bucket,
            dimensions = build_dims,
            properties = build_props,
            service_account = env.worker_sa,
            allowed_property_overrides = ["go_commit"],
        )

    # Test builder.
    test_dims = dict(base_dims)
    test_props = dict(base_props)
    test_props.update({
        "mode": GOLANGBUILD_MODES["TEST"],
        "test_mode": {},
        # For sharded x repo builders, go_commit will be overwritten by the coordinator builder.
        "go_commit": "",
        # The default is no sharding. This may be overwritten by the coordinator builder.
        "test_shard": {"shard_id": 0, "num_shards": 1},
    })
    emit_builder(
        name = test_name,
        bucket = env.worker_bucket,
        dimensions = test_dims,
        properties = test_props,
        service_account = env.worker_sa,
        allowed_property_overrides = ["go_commit", "test_shard"],
    )
    return builders_to_trigger

def define_allmode_builder(env, name, builder_type, base_props, base_dims, emit_builder):
    # Create an "ALL" mode builder which just performs the full build serially.
    #
    # This builder is substantially simpler because it doesn't need to trigger any
    # downstream builders, and also doesn't need test sharding (most subrepos' tests
    # run fast enough that it's unnecessary).
    all_props = dict(base_props)
    all_props.update({
        "mode": GOLANGBUILD_MODES["ALL"],
    })
    emit_builder(
        name = name,
        bucket = env.bucket,
        dimensions = base_dims,
        properties = all_props,
        triggering_policy = triggering_policy(env, builder_type),
        service_account = env.worker_sa,
    )

def define_perfmode_builder(env, name, builder_type, base_props, base_dims, emit_builder):
    # Create an "PERF" mode builder which runs benchmarks. Its operation is somewhat
    # special, usually involving checking out two copies of a repository and possibly
    # a third containing benchmarks.
    #
    # This builder is substantially simpler because it doesn't need to trigger any
    # downstream builders, and also doesn't currently support sharding.
    perf_props = dict(base_props)
    perf_props.update({
        "mode": GOLANGBUILD_MODES["PERF"],
    })
    perf_dims = dict(base_dims)
    timeout = 12 * time.hour
    if "pgo" in perf_props["perf_mode"] and perf_props["perf_mode"]["pgo"] == True:
        # Bump the timeout for PGO builders. By construction, they take 3x as long to run.
        timeout = 36 * time.hour

    emit_builder(
        name = name,
        bucket = env.bucket,
        dimensions = perf_dims,
        properties = perf_props,
        triggering_policy = triggering_policy(env, builder_type, concurrent_builds = 3),
        service_account = env.worker_sa,
        execution_timeout = timeout,
    )

# triggering_policy defines the LUCI Scheduler triggering policy for postsubmit builders.
# Returns None for presubmit builders.
def triggering_policy(env, builder_type, concurrent_builds = 5):
    if env.bucket not in ["ci", "ci-workers"]:
        return None
    if is_capacity_constrained(env.low_capacity_hosts, host_of(builder_type)):
        concurrent_builds = 1
    return scheduler.newest_first(
        max_concurrent_invocations = concurrent_builds,
        pending_timeout = time.week,
    )

def display_for_builder_type(builder_type):
    """Produces the category and short name for a luci.console_view_entry.

    Args:
        builder_type: A name defined in `BUILDER_TYPES`.

    Returns:
        The category and the short name.
    """
    components = builder_type.split("-", 2)
    short_name = components[2] if len(components) > 2 else None
    category = "|".join(components[:2])
    return category, short_name  # Produces: "$GOOS|$GOARCH", $HOST_SPECIFIER(-$RUN_MOD)*

# enabled returns three values and a list or None.
#
# The first value is a boolean which indicates if this builder_type should exist at all for the
# given project and branch.
#
# The second is a PRESUBMIT enum value that indicates whether the builder_type should be disabled,
# optional, or enabled for presubmit.
#
# The third value is a boolean which indicates if this builder_type should run in postsubmit.
#
# The final list is a list of cq.location_filters if the second value is PRESUBMIT.ENABLED.
def enabled(low_capacity_hosts, project, go_branch_short, builder_type, known_issue_builder_types):
    pt = PROJECTS[project]
    os, arch, suffix, run_mods = split_builder_type(builder_type)
    host_type = host_of(builder_type)

    if builder_type in known_issue_builder_types and known_issue_builder_types[builder_type] \
        .skip_x_repos and project != "go":
        return False, PRESUBMIT.DISABLED, False, []

    # Filter out old OS versions from new branches.
    if os == "darwin" and suffix == "11" and go_branch_short not in ["go1.24"]:
        # Go 1.24 is last to support macOS 11. See go.dev/doc/go1.24#darwin.
        return False, PRESUBMIT.DISABLED, False, []
    elif os == "darwin" and suffix == "12" and go_branch_short not in ["go1.24", "go1.25", "go1.26"]:
        # Go 1.26 is last to support macOS 12. See go.dev/issue/75836.
        return False, PRESUBMIT.DISABLED, False, []

    # Filter out new ports on old release branches.
    if os == "freebsd" and arch == "amd64" and "race" in run_mods and go_branch_short in ["go1.24"]:
        # The freebsd-amd64-race LUCI builders may need fixes that aren't available on old branches.
        return False, PRESUBMIT.DISABLED, False, []

    # Docker builder should only be used in VSCode-Go repo.
    if suffix == "docker" and project != "vscode-go":
        return False, PRESUBMIT.DISABLED, False, []
    if suffix != "docker" and project == "vscode-go":
        return False, PRESUBMIT.DISABLED, False, []

    # Only run avx512 builders on the main Go repository, there's little value gained elsewhere.
    if suffix == "avx512" and project != "go":
        return False, PRESUBMIT.DISABLED, False, []

    # Apply basic policies about which projects run on what machine types,
    # and what we have capacity to run in presubmit.
    enable_types = None
    if pt == PT.TOOL:
        enable_types = ["linux-amd64", "windows-amd64", "darwin-amd64"]
    elif project == "mobile":
        enable_types = ["linux-amd64", "android"]
    elif project == "exp":
        # Not quite sure what to do with exp/shiny. For now just run on major platforms.
        enable_types = ["linux-386", "linux-amd64", "linux-arm64", "windows-386", "windows-amd64", "darwin-amd64"]
    elif project in ["protobuf", "open2opaque"]:
        enable_types = ["linux-amd64"]  # See issue go.dev/issue/63597.
    elif project == "vscode-go":
        enable_types = ["linux-amd64"]
    elif pt == PT.SPECIAL:
        fail("unhandled SPECIAL project: %s" % project)
    postsubmit = enable_types == None or any([x == "%s-%s" % (os, arch) for x in enable_types])
    presubmit = postsubmit  # Default to running in presubmit if and only if running in postsubmit.
    presubmit = presubmit and not is_capacity_constrained(low_capacity_hosts, host_type)  # Capacity.
    presubmit = presubmit and not host_timeout_scale(host_type) > 1  # Speed.
    presubmit = presubmit and not ("longtest" in run_mods and "race" in run_mods)  # Speed.
    presubmit = presubmit and not builder_type in known_issue_builder_types  # Known issues.
    presubmit = presubmit and (is_first_class(os, arch) or arch == "wasm")  # Only first-class ports or wasm.
    if project != "go":  # Some ports run as presubmit only in the main Go repo.
        presubmit = presubmit and os not in ["js", "wasip1"]

    # Apply policies for each run mod.
    presubmit_filters = None
    for mod in run_mods:
        ex, pre, post, prefilt = RUN_MODS[mod].enabled(port_of(builder_type), project, go_branch_short)
        if not ex:
            return False, False, False, []
        presubmit = presubmit and pre
        postsubmit = postsubmit and post

        # Intersect the presubmit filters to be conservative about where
        # builders run in presubmit.
        #
        # This is a fairly rudimentary and overly conservative intersection.
        # Ideally we'd also intersect the filter regexps, but that's more complex.
        if prefilt and presubmit_filters == None:
            presubmit_filters = set(prefilt)
        elif not prefilt:
            presubmit_filters = set()  # Intersection with nothing is nothing.
        else:
            presubmit_filters = presubmit_filters.intersection(prefilt)

    # Convert presubmit filters to a list, which the caller expects.
    if presubmit_filters:
        presubmit_filters = list(presubmit_filters)
    else:
        presubmit_filters = []

    # Disable presubmit and postsubmit if given (project, Go branch) pair is out of scope of slow hosts.
    if builder_type in SLOW_HOSTS and hasattr(SLOW_HOSTS[builder_type], "scope"):
        if "%s@%s" % (project, go_branch_short) not in SLOW_HOSTS[builder_type].scope and \
           project not in SLOW_HOSTS[builder_type].scope:
            presubmit, postsubmit = False, False

    # Hide from presubmit if required.
    presubmit_state = PRESUBMIT.ENABLED if presubmit else PRESUBMIT.OPTIONAL
    if builder_type in known_issue_builder_types and known_issue_builder_types[builder_type].hide_from_presubmit:
        presubmit_state = PRESUBMIT.DISABLED

    return True, presubmit_state, postsubmit, presubmit_filters

# Apply LUCI-TryBot-Result +1 or -1 based on CQ result.
#
# TODO(prattmic): Switch to TryBot-Result once we are ready to use these for
# submit enforcement.
POST_ACTIONS = [
    cq.post_action_gerrit_label_votes(
        name = "trybot-success",
        conditions = [
            cq.post_action_triggering_condition(
                mode = cq.MODE_DRY_RUN,
                statuses = [cq.STATUS_SUCCEEDED],
            ),
        ],
        labels = {"LUCI-TryBot-Result": 1},
    ),
    cq.post_action_gerrit_label_votes(
        name = "trybot-failure",
        conditions = [
            cq.post_action_triggering_condition(
                mode = cq.MODE_DRY_RUN,
                statuses = [cq.STATUS_FAILED, cq.STATUS_CANCELLED],
            ),
        ],
        labels = {"LUCI-TryBot-Result": -1},
    ),
]

def _define_go_ci():
    # Presubmit.
    for project in PROJECTS:
        for go_branch_short, go_branch in GO_BRANCHES.items():
            # Set up a CQ group for the builder definitions below.
            cq_group = go_cq_group(project, go_branch_short)
            luci.cq_group(
                name = cq_group.name,
                acls = [
                    acl.entry(
                        roles = acl.CQ_DRY_RUNNER,
                        groups = ["project-golang-may-start-trybots"],
                    ),
                    acl.entry(
                        roles = acl.CQ_COMMITTER,
                        groups = ["project-golang-approvers"],
                    ),
                ],
                watch = cq_group.watch,
                allow_submit_with_open_deps = True,
                trust_dry_runner_deps = True,
                allow_non_owner_dry_runner = True,
                post_actions = POST_ACTIONS,
            )

            # Define builders.
            for builder_type in BUILDER_TYPES:
                exists, presubmit, _, presubmit_filters = enabled(LOW_CAPACITY_HOSTS, project, go_branch_short, builder_type, KNOWN_ISSUE_BUILDER_TYPES)
                if not exists or presubmit == PRESUBMIT.DISABLED:
                    continue

                name, _, _ = define_builder(PUBLIC_TRY_ENV, project, go_branch_short, builder_type)
                luci.cq_tryjob_verifier(
                    builder = name,
                    cq_group = cq_group.name,
                    includable_only = presubmit == PRESUBMIT.OPTIONAL,
                    disable_reuse = True,
                    location_filters = [
                        cq.location_filter(
                            gerrit_host_regexp = "go-review.googlesource.com",
                            gerrit_project_regexp = "^%s$" % project,
                            path_regexp = filter,
                        )
                        for filter in presubmit_filters
                        if presubmit == PRESUBMIT.ENABLED
                    ],
                )

                # For the main Go repo, include the ability to run presubmit
                # against any golang.org/x repo.
                # Make presubmit enabled by default for a few select cases.
                if project != "go":
                    # As a baseline, all golang.org/x repo builders (which aren't
                    # disabled for presubmit) are available in the main Go repo
                    # for presubmit as optional. Some adjustments are made below.
                    go_repo_presubmit = PRESUBMIT.OPTIONAL
                    location_filters = None

                    # Add an x/tools builder to the Go presubmit by default.
                    if project == "tools" and builder_type == "linux-amd64":
                        go_repo_presubmit = PRESUBMIT.ENABLED

                    # Add an x/debug builder to the Go presubmit by default.
                    # It mostly only matters when changes to cmd/compile, cmd/link,
                    # or the runtime are made, but certain satellite packages like
                    # internal/abi also matter. Just enable it for everything,
                    # it's a fast builder.
                    if project == "debug" and builder_type == "linux-amd64":
                        go_repo_presubmit = PRESUBMIT.ENABLED

                    # Add an x/website builder to the Go presubmit
                    # but only when release notes are being edited.
                    # This is to catch problems with Markdown/HTML.
                    # See go.dev/issue/68633.
                    if project == "website" and builder_type == "linux-amd64" and go_branch_short == "gotip":
                        go_repo_presubmit = PRESUBMIT.ENABLED
                        location_filters = [
                            cq.location_filter(
                                gerrit_host_regexp = "go-review.googlesource.com",
                                gerrit_project_regexp = "^go$",
                                path_regexp = "doc/next/.+",
                            ),
                        ]

                    luci.cq_tryjob_verifier(
                        builder = name,
                        cq_group = go_cq_group("go", go_branch_short).name,
                        includable_only = go_repo_presubmit == PRESUBMIT.OPTIONAL,
                        disable_reuse = True,
                        location_filters = location_filters,
                    )

                # For golang.org/x repos, include the ability to run presubmit
                # against all supported releases in addition to testing with tip.
                # Make presubmit enabled by default for builders deemed "fast".
                # See go.dev/issue/17626.
                if project != "go" and go_branch_short != "gotip":
                    first_class_subset = builder_type in ["linux-amd64", "linux-386", "darwin-amd64_14", "windows-amd64"]
                    vscode_go_special_case = project == "vscode-go" and builder_type == "linux-amd64_docker"

                    in_postsubmit = enabled(LOW_CAPACITY_HOSTS, project, go_branch_short, builder_type, KNOWN_ISSUE_BUILDER_TYPES)[2]

                    x_repo_presubmit = in_postsubmit and (first_class_subset or vscode_go_special_case)
                    luci.cq_tryjob_verifier(
                        builder = name,
                        cq_group = go_cq_group(project, "gotip").name,
                        includable_only = not x_repo_presubmit,
                        disable_reuse = True,
                    )

            # For golang.org/x/tools, also include coverage for extra Go versions.
            if project == "tools" and go_branch_short == "gotip":
                for extra_go_release, _ in TOOLS_GO_BRANCHES.items():
                    builder_type = "linux-amd64"  # Just one fast and highly available builder is deemed enough.
                    try_builder, _, _ = define_builder(PUBLIC_TRY_ENV, project, extra_go_release, builder_type)
                    luci.cq_tryjob_verifier(
                        builder = try_builder,
                        cq_group = cq_group.name,
                        disable_reuse = True,
                    )

    # Postsubmit.
    console_generators = []
    postsubmit_builders_by_port = {}
    postsubmit_builders_by_project_and_branch = {}
    postsubmit_builders_with_go_repo_trigger = {}
    for project in PROJECTS:
        for go_branch_short, go_branch in GO_BRANCHES.items():
            # Define builders.
            postsubmit_builders = {}
            postsubmit_builders_known_issue = {}
            for builder_type in BUILDER_TYPES:
                exists, _, postsubmit, _ = enabled(LOW_CAPACITY_HOSTS, project, go_branch_short, builder_type, KNOWN_ISSUE_BUILDER_TYPES)
                if not exists or not postsubmit:
                    continue

                name, triggers, known_issue = define_builder(PUBLIC_CI_ENV, project, go_branch_short, builder_type)
                if known_issue != 0:
                    postsubmit_builders_known_issue[name] = struct(builder_type = builder_type, known_issue = known_issue)
                else:
                    postsubmit_builders[name] = struct(builder_type = builder_type)

                # Collect all the builders that have triggers from the Go repository. Every builder needs at least one.
                if project == "go":
                    postsubmit_builders_with_go_repo_trigger[name] = True
                    for name in triggers:
                        postsubmit_builders_with_go_repo_trigger[name] = True

            # For golang.org/x/tools, also include coverage for extra Go versions.
            if project == "tools" and go_branch_short == "gotip":
                for extra_go_release, _ in TOOLS_GO_BRANCHES.items():
                    builder_type = "linux-amd64"  # Just one fast and highly available builder is deemed enough.
                    ci_builder, _, _ = define_builder(PUBLIC_CI_ENV, project, extra_go_release, builder_type)
                    postsubmit_builders[ci_builder] = struct(builder_type = builder_type)
                    postsubmit_builders_with_go_repo_trigger[ci_builder] = True

            # Collect all the postsubmit builders by port and project.
            for name, b in postsubmit_builders.items() + postsubmit_builders_known_issue.items():
                os, arch, _, _ = split_builder_type(b.builder_type)
                port = "%s/%s" % (os, arch)
                postsubmit_builders_by_port.setdefault(port, []).append(name)
                postsubmit_builders_by_project_and_branch.setdefault((project, go_branch.branch, go_branch_short), []).append(name)

            # Create the gitiles_poller last because we need the full set of builders to
            # trigger at the point of definition.
            #
            # This is the poller for commits coming in from the target repo. Subrepo builders are
            # also triggered by the main Go repo builds in one of two ways. Either the main repo builds
            # trigger them directly once the toolchain builder is done, or that builder is not added to
            # list of builders with triggers and we generate pollers for those builders at the end.
            if project == "go":
                poller_branch = go_branch.branch
            else:
                poller_branch = MAIN_BRANCH_NAME
            luci.gitiles_poller(
                name = "%s-%s-trigger" % (project, go_branch_short),
                bucket = "ci",
                repo = "https://go.googlesource.com/%s" % project,
                refs = ["refs/heads/" + poller_branch],
                triggers = postsubmit_builders.keys() + postsubmit_builders_known_issue.keys(),
            )

            # Create consoles generators.
            if project == "go":
                console_generators.extend([
                    # Main console for go on go_branch_short.
                    make_console_gen(project, go_branch_short, postsubmit_builders),
                    # Known issue builders console for go on go_branch_short.
                    make_console_gen(project, go_branch_short, postsubmit_builders_known_issue, known_issue = True, tier = 3),
                ])
            else:
                console_generators.extend([
                    # Main console for project on go_branch_short.
                    make_console_gen(project, go_branch_short, postsubmit_builders),
                    # Console for project on go_branch_short ordered by Go commit.
                    make_console_gen(project, go_branch_short, postsubmit_builders, by_go_commit = True, tier = 2),
                    # Known issue builders console for project on go_branch_short ordered.
                    make_console_gen(project, go_branch_short, postsubmit_builders_known_issue, known_issue = True, tier = 3),
                    # Known issue builders console for project on go_branch_short ordered by Go commit.
                    make_console_gen(project, go_branch_short, postsubmit_builders_known_issue, by_go_commit = True, known_issue = True, tier = 4),
                ])

    # Collect all the postsubmit builders that still need triggers.
    postsubmit_builders_need_triggers = {}
    for target, builders in postsubmit_builders_by_project_and_branch.items():
        for name in builders:
            if name not in postsubmit_builders_with_go_repo_trigger:
                postsubmit_builders_need_triggers.setdefault(target, []).append(name)

    # Emit gitiles_pollers for all the builders who don't have triggers.
    for target, builders in postsubmit_builders_need_triggers.items():
        project, go_branch, go_branch_short = target
        if project == "go":
            fail("discovered main Go repo builders without triggers: %s" % builders)
        luci.gitiles_poller(
            name = "%s-%s-go-trigger" % (project, go_branch_short),
            bucket = "ci",
            repo = "https://go.googlesource.com/go",
            refs = ["refs/heads/" + go_branch],
            triggers = builders,
        )

    # Emit consoles in order of tier, and within each tier, the order in which
    # we iterated over PROJECTS and GO_BRANCHES. (sorted is a stable sort.)
    # The order we emit them here will be the display order.
    for console in sorted(console_generators, key = lambda c: c.tier):
        console.gen()

    # Emit builder groups for each port.
    # These will appear last, since they're emitted last.
    for port, builders in postsubmit_builders_by_port.items():
        luci.list_view(
            name = "port-%s" % port.replace("/", "-"),
            title = "all %s" % port,
            entries = builders,
        )

def _define_go_internal_ci():
    for project_name in ["go", "net", "crypto", "oauth2", "build"]:
        for go_branch_short, go_branch in INTERNAL_GO_BRANCHES.items():
            cq_group_name = ("go-internal_%s_%s" % (project_name, go_branch_short)).replace(".", "-")
            luci.cq_group(
                name = cq_group_name,
                acls = [
                    acl.entry(
                        roles = acl.CQ_DRY_RUNNER,
                        groups = ["mdb/golang-security-policy", "mdb/golang-release-eng-policy"],
                    ),
                    acl.entry(
                        roles = acl.CQ_COMMITTER,
                        groups = ["mdb/golang-security-policy", "mdb/golang-release-eng-policy"],
                    ),
                ],
                watch = cq.refset(
                    repo = "https://go-internal.googlesource.com/%s" % project_name,
                    refs = ["refs/heads/%s" % branch for branch in go_branch.branch_regexps],
                ),
                allow_submit_with_open_deps = True,
                trust_dry_runner_deps = True,
                allow_non_owner_dry_runner = True,
                # TODO(prattmic): Set post_actions to apply TryBot-Result labels.
            )

            for builder_type in BUILDER_TYPES:
                exists, presubmit, postsubmit, _ = enabled(LOW_CAPACITY_HOSTS, project_name, go_branch_short, builder_type, SECURITY_KNOWN_ISSUE_BUILDER_TYPES)
                host_type = host_of(builder_type)
                if host_type in DEFAULT_HOST_SUFFIX:
                    host_type += "_" + DEFAULT_HOST_SUFFIX[host_type]

                # The internal host only has access to some machines. As of
                # writing, that means all the GCE-hosted (high-capacity) builders
                # and a select subset of GOOGLE_LOW_CAPACITY_HOSTS, and that's it.
                if host_type in [
                    # A select subset of GOOGLE_LOW_CAPACITY_HOSTS that provide
                    # a separate set of VMs that connect to the chrome-swarming
                    # swarming instance. This requires additional resources and
                    # work to set up, hence each such host needs to opt-in here.
                    "linux-arm",
                    "darwin-amd64_11",
                    "darwin-amd64_12",
                    "darwin-amd64_13",
                    "darwin-amd64_14",
                    "darwin-arm64_14",
                    "darwin-arm64_15",
                ]:
                    # The list above is expected to contain only Google low-capacity hosts.
                    # Verify that's the case.
                    if not is_capacity_constrained(GOOGLE_LOW_CAPACITY_HOSTS, host_type):
                        fail("host type %s is listed explicitly but is not a Google low-capacity host", host_type)
                elif is_capacity_constrained(LOW_CAPACITY_HOSTS, host_type):
                    exists = False

                if not exists:
                    continue

                # Define presubmit builders. Since there's no postsubmit to monitor,
                # all possible completed builders that perform testing are required.
                name, _, _ = define_builder(SECURITY_TRY_ENV, project_name, go_branch_short, builder_type)
                _, _, _, run_mods = split_builder_type(builder_type)
                if presubmit != PRESUBMIT.DISABLED:
                    luci.cq_tryjob_verifier(
                        builder = name,
                        cq_group = cq_group_name,
                        disable_reuse = True,
                        includable_only = any([r.startswith("perf") for r in run_mods]) or
                                          (presubmit == PRESUBMIT.OPTIONAL and not postsubmit) or
                                          builder_type in SECURITY_KNOWN_ISSUE_BUILDER_TYPES,
                    )

_define_go_ci()
_define_go_internal_ci()

exec("./recipes.star")
