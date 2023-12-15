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
    tricium = "tricium-prod.appspot.com",
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
                "tricium-prod@appspot.gserviceaccount.com",
                "coordinator-builder@golang-ci-luci.iam.gserviceaccount.com",
                "security-coordinator-builder@golang-ci-luci.iam.gserviceaccount.com",
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

def define_environment(gerrit_host, swarming_host, bucket, coordinator_sa, worker_sa, low_capacity):
    return struct(
        gerrit_host = gerrit_host,
        swarming_host = swarming_host + ".appspot.com",
        bucket = bucket,
        worker_bucket = bucket + "-workers",
        coordinator_sa = coordinator_sa + "@golang-ci-luci.iam.gserviceaccount.com",
        coordinator_pool = "luci.golang.%s" % bucket,
        worker_sa = worker_sa + "@golang-ci-luci.iam.gserviceaccount.com",
        worker_pool = "luci.golang.%s-workers" % bucket,
        shared_worker_pool = "luci.golang.shared-workers",
        low_capacity_hosts = low_capacity,
    )

# GOOGLE_LOW_CAPACITY_HOSTS are low-capacity hosts that happen to be operated
# by Google, so we can rely on them being available.
GOOGLE_LOW_CAPACITY_HOSTS = [
    "darwin-amd64_10.15",
    "darwin-amd64_11",
    "darwin-amd64_12",
    "darwin-amd64_13",
    "darwin-amd64_14",
    "darwin-arm64_11",
    "darwin-arm64_12",
    "darwin-arm64_13",
    "windows-arm64",
]

# LOW_CAPACITY_HOSTS lists "hosts" that have fixed, relatively low capacity.
# They need to match the builder type, excluding any run mods.
LOW_CAPACITY_HOSTS = GOOGLE_LOW_CAPACITY_HOSTS + [
    "freebsd-riscv64",
    "linux-ppc64",
    "linux-ppc64le",
    "linux-riscv64",
    "netbsd-arm",
    "netbsd-arm64",
    "openbsd-ppc64",
    "openbsd-riscv64",
    "plan9-386",
    "plan9-amd64",
    "plan9-arm",
    "solaris-amd64",
]

# DEFAULT_HOST_SUFFIX defines the default host suffixes for builder types which
# do not specify one.
DEFAULT_HOST_SUFFIX = {
    "darwin-amd64": "14",
    "linux-amd64": "debian11",
    "linux-arm64": "debian11",
    "openbsd-amd64": "7.2",
    "windows-386": "10",
    "windows-amd64": "10",
}

# The try bucket will include builders which work on pre-commit or pre-review
# code.
PUBLIC_TRY_ENV = define_environment("go", "chromium-swarm", "try", "coordinator-builder", "public-worker-builder", LOW_CAPACITY_HOSTS)

# The ci bucket will include builders which work on post-commit code.
PUBLIC_CI_ENV = define_environment("go", "chromium-swarm", "ci", "coordinator-builder", "public-worker-builder", LOW_CAPACITY_HOSTS)

# The security-try bucket is for builders that test unreviewed, embargoed
# security fixes.
SECURITY_TRY_ENV = define_environment("go-internal", "chrome-swarming", "security-try", "security-coordinator-builder", "security-worker-builder", LOW_CAPACITY_HOSTS)

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
# specifies the OS version, and a series of run-time modifications
# (listed in RUN_MODS).
#
# The format of a builder type is thus $GOOS-$GOARCH(_osversion)?(-$RUN_MOD)*.
BUILDER_TYPES = [
    "darwin-amd64-nocgo",
    "darwin-amd64_10.15",
    "darwin-amd64_11",
    "darwin-amd64_12",
    "darwin-amd64_13",
    "darwin-amd64_14",
    "darwin-arm64_11",
    "darwin-arm64_12",
    "darwin-arm64_13",
    "freebsd-riscv64",
    "js-wasm",
    "linux-386",
    "linux-386-longtest",
    "linux-amd64",
    "linux-amd64-boringcrypto",
    "linux-amd64-longtest",
    "linux-amd64-longtest-race",
    "linux-amd64-misccompile",
    "linux-amd64-newinliner",
    "linux-amd64-nocgo",
    "linux-amd64-race",
    "linux-amd64-staticlockranking",
    "linux-arm64",
    "linux-ppc64-power10",
    "linux-ppc64le",
    "linux-riscv64",
    "netbsd-arm",
    "netbsd-arm64",
    "openbsd-amd64",
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

# NO_NETWORK_BUILDERS are a subset of builder types
# where we require the no-network check to run.
NO_NETWORK_BUILDERS = [
    "linux-386",
    "linux-amd64",
]

# make_run_mod returns a run_mod that adds the given properties and environment
# variables. If set, enabled is a function that returns three booleans to
# affect the builder it's being added to:
# - exists, whether the builder should be created at all
# - presubmit, whether the builder should be run in presubmit by default
# - postsubmit, whether the builder should run in postsubmit
# - presubmit location filters, any cq.location_filter to apply to presubmit
def make_run_mod(add_props = {}, add_env = {}, enabled = None):
    def apply_mod(props):
        props.update(add_props)
        props["env"].update(add_env)

    if enabled == None:
        enabled = lambda port, project, go_branch_short: (True, True, True, [])
    return struct(
        enabled = enabled,
        apply = apply_mod,
    )

# enable only if project matches one of the provided projects and certain source
# locations are modified by the CL, or always for the release branches of the go project.
# projects is a dict mapping a project name to filters.
def presubmit_only_for_projs_or_on_release_branches(projects):
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
def presubmit_only_for_ports_or_on_release_branches(ports):
    def f(port, project, go_branch_short):
        presubmit = port in ports or (project == "go" and go_branch_short != "gotip")
        return (True, presubmit, True, [])

    return f

# define the builder only for the go project at versions after x, useful for
# non-default build modes that were created at x.
def define_for_go_starting_at(x):
    def f(port, project, go_branch_short):
        run = project == "go" and (go_branch_short == "gotip" or go_branch_short >= x)
        return (run, run, run, [])

    return f

# define the builder only for postsubmit of the go project.
def define_for_go_postsubmit():
    def f(port, project, go_branch_short):
        run = project == "go"
        return (run, False, run, [])

    return f

# define the builder only for postsubmit of the go project, or for presubmit
# of the go project if a particular location is touched.
def define_for_go_postsubmit_or_presubmit_with_filters(filters):
    def f(port, project, go_branch_short):
        run = project == "go"
        return (run, run, run, filters)

    return f

# RUN_MODS is a list of valid run-time modifications to the way we
# build and test our various projects.
RUN_MODS = dict(
    longtest = make_run_mod({"long_test": True}, {"GO_TEST_TIMEOUT_SCALE": "5"}, enabled = presubmit_only_for_projs_or_on_release_branches({
        "protobuf": [],
        "go": [
            # Enable longtest builders on go against tip if files related to vendored code are modified.
            "src/{,cmd/}go[.]{mod,sum}",
            "src/{,cmd/}vendor/.+",
            "src/.+_bundle.go",
        ],
    })),
    race = make_run_mod({"race_mode": True}, enabled = presubmit_only_for_ports_or_on_release_branches(["linux-amd64"])),
    boringcrypto = make_run_mod(add_env = {"GOEXPERIMENT": "boringcrypto"}),
    # The misccompile mod indicates that the builder should act as a "misc-compile" builder,
    # that is to cross-compile all non-first-class ports to quickly flag portability issues.
    misccompile = make_run_mod({"compile_only": True, "misc_ports": True}),
    newinliner = make_run_mod(add_env = {"GOEXPERIMENT": "newinliner"}, enabled = define_for_go_starting_at("go1.22")),
    nocgo = make_run_mod(add_env = {"CGO_ENABLED": "0"}, enabled = define_for_go_postsubmit()),
    staticlockranking = make_run_mod(add_env = {"GOEXPERIMENT": "staticlockranking"}, enabled = define_for_go_postsubmit_or_presubmit_with_filters(["src/runtime/[^/]+"])),
    power10 = make_run_mod(add_env = {"GOPPC64": "power10"}),
)

# PT is Project Type, a classification of a project.
PT = struct(
    CORE = "core",  # The Go project or something that it depends on. Needs to be tested everywhere.
    LIBRARY = "library",  # A user-facing library. Needs to be tested on a representative set of platforms.
    TOOL = "tool",  # A developer tool. Typically only run on mainstream platforms such as Linux, MacOS, and Windows.
    SPECIAL = "special",  # None of the above; something that needs a handcrafted set.
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
    "exp": PT.SPECIAL,
    "image": PT.LIBRARY,
    "mobile": PT.SPECIAL,
    "mod": PT.CORE,
    "net": PT.CORE,
    "oauth2": PT.LIBRARY,
    "perf": PT.TOOL,
    "pkgsite": PT.TOOL,
    "pkgsite-metrics": PT.TOOL,
    "protobuf": PT.SPECIAL,
    "review": PT.TOOL,
    "sync": PT.CORE,
    "sys": PT.CORE,
    "telemetry": PT.TOOL,
    "term": PT.CORE,
    "text": PT.CORE,
    "time": PT.LIBRARY,
    "tools": PT.LIBRARY,
    "vuln": PT.TOOL,
    "vulndb": PT.TOOL,
    "website": PT.TOOL,
}

# EXTRA_DEPENDENCIES specifies custom additional dependencies
# to append when applies(project, port, run_mods) matches.
EXTRA_DEPENDENCIES = [
    # The protobuf repo needs extra dependencies for its integration test.
    # See its integration_test.go file and go.dev/issue/64066.
    struct(
        applies = lambda project, port, run_mods: project == "protobuf" and port == "linux-amd64" and "longtest" in run_mods,
        test_deps = """@Subdir bin
golang/third_party/protoc_with_conformance/${platform} version:25.0-rc2
""",
    ),
]

# GO_BRANCHES lists the branches of the "go" project to build and test against.
# Keys in this map are shortened aliases while values are the git branch name.
GO_BRANCHES = {
    "gotip": struct(branch = "master", bootstrap = "1.20.6"),
    "go1.22": struct(branch = "release-branch.go1.22", bootstrap = "1.17.13"),
    "go1.21": struct(branch = "release-branch.go1.21", bootstrap = "1.17.13"),
    "go1.20": struct(branch = "release-branch.go1.20", bootstrap = "1.17.13"),
}

# EXTRA_GO_BRANCHES are Go branches that aren't used for project-wide testing
# because they're out of scope per https://go.dev/doc/devel/release#policy,
# but are used by specific golang.org/x repositories.
EXTRA_GO_BRANCHES = {
    "go1.19": struct(branch = "release-branch.go1.19", bootstrap = "1.17.13"),
    "go1.18": struct(branch = "release-branch.go1.18", bootstrap = "1.17.13"),
}

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

    host = "%s-%s" % (goos, goarch)

    # We run some 386 ports on amd64 machines.
    if goarch == "386" and goos in ("linux", "windows"):
        host = host.replace("386", "amd64")

    os = None

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
        if goos == "linux":
            # linux-amd64_debian11 -> Debian-11
            os = suffix.replace("debian", "Debian-")
        elif goos == "darwin":
            # darwin-amd64_12.6 -> Mac-12.6
            os = "Mac-" + suffix
        elif goos == "windows":
            os = "Windows-" + suffix
        elif goos == "openbsd":
            os = "openbsd-" + suffix

    dims = {"cipd_platform": host.replace("darwin", "mac")}
    if os != None:
        dims["os"] = os
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
    supported = ["%s-%s" % (os, arch) for os in ["linux", "mac", "windows"] for arch in ["amd64", "arm64"]]
    return dims["cipd_platform"] in supported

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

    elif project in ["dl", "protobuf"]:
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
    elif project == "protobuf":
        return "google.golang.org/protobuf"
    else:
        # A golang.org/x/* repository. Since these are very common,
        # the 'golang.org/' prefix is left out for brevity.
        return "x/" + project

# console_name produces the console name for the given project and branch.
def console_name(project, go_branch_short, suffix):
    if project == "go":
        fail("console_name doesn't have support for the 'go' project")
    sort_letter = "x"
    if not project_title(project).startswith("x/"):
        # Put "z" at the beginning to sort this at the bottom of the page.
        sort_letter = "z"
    return "%s-%s-%s" % (sort_letter, project, go_branch_short) + suffix

# Enum values for golangbuild's "mode" property.
GOLANGBUILD_MODES = {
    "ALL": 0,
    "COORDINATOR": 1,
    "BUILD": 2,
    "TEST": 3,
}

def define_builder(env, project, go_branch_short, builder_type):
    """Creates a builder definition.

    Args:
        env: the environment the builder runs in.
        project: A go project defined in `PROJECTS`.
        go_branch_short: A go repository branch name defined in `GO_BRANCHES` or `EXTRA_GO_BRANCHES`.
        builder_type: A name defined in `BUILDER_TYPES`.

    Returns:
        The full name including a bucket prefix.
        A list of the builders this builder will trigger (by full name).
    """

    os, arch, suffix, run_mods = split_builder_type(builder_type)
    host_type = host_of(builder_type)
    hostos, hostarch, _, _ = split_builder_type(host_type)

    # Contruct the basic properties that will apply to all builders for
    # this combination.
    known_go_branches = dict(GO_BRANCHES)
    known_go_branches.update(EXTRA_GO_BRANCHES)
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
    for d in EXTRA_DEPENDENCIES:
        if not d.applies(project, port_of(builder_type), run_mods):
            continue
        if d.test_deps != "":
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
            if go_branch_short == "go1.20":
                base_props["node_version"] = "13.2.0"
        elif os == "wasip1":
            if suffix == "wasmtime":
                base_props["env"]["GOWASIRUNTIME"] = "wasmtime"
                base_props["wasmtime_version"] = "2@14.0.4"
                if go_branch_short == "go1.21":
                    base_props["wasmtime_version"] = "13.0.1"  # See go.dev/issue/63718.
            elif suffix == "wazero":
                base_props["env"]["GOWASIRUNTIME"] = "wazero"
                base_props["wazero_version"] = "2@1.5.0"
            else:
                fail("unknown GOOS=wasip1 builder suffix: %s" % suffix)

    # Construct the basic dimensions for the build/test running part of the build.
    #
    # Note that these should generally live in the worker pools.
    base_dims = dimensions_of(host_type)
    base_dims["pool"] = env.worker_pool
    if is_capacity_constrained(env.low_capacity_hosts, host_type):
        # Scarce resources live in the shared-workers pool.
        base_dims["pool"] = env.shared_worker_pool

    # On less-supported platforms, we may not have bootstraps before 1.21
    # started cross-compiling everything.
    if not is_fully_supported(base_dims) and (base_props["bootstrap_version"].startswith("1.20") or base_props["bootstrap_version"].startswith("1.1")):
        base_props["bootstrap_version"] = "1.21.0"

    if os == "darwin":
        # See available versions with: cipd instances -limit 0 infra_internal/ios/xcode/mac | less
        xcode_versions = {
            10: "11e503a",  # released Apr 2020, macOS 10.15 released Oct 2019
            11: "12b45b",  # released Nov 2020, macOS 11 released Nov 2020
            12: "13c100",  # released Dec 2021, macOS 12 released Oct 2021
            13: "14c18",  # released Dec 2022, macOS 13 released Oct 2022
            14: "15a240d",  # released Sep 2023, macOS 14 released Sep 2023
        }
        base_props["xcode_version"] = xcode_versions[int(dimensions_of(host_type)["os"].split(".")[0].replace("Mac-", ""))]

    # Turn on the no-network check.
    if builder_type in NO_NETWORK_BUILDERS:
        base_props["no_network"] = True

        # Leave release branches out of scope, they can't work until some
        # test fixes are backported, but doing that might not be worth it.
        # TODO(dmitshur): Delete this after Go 1.21 drops off.
        if project == "go" and (go_branch_short == "go1.21" or go_branch_short == "go1.20"):
            base_props.pop("no_network")

    for mod in run_mods:
        if not mod in RUN_MODS:
            fail("unknown run mod: %s" % mod)
        RUN_MODS[mod].apply(base_props)

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
    }

    # Construct the executable reference.
    executable = luci.executable(
        name = "golangbuild",
        cipd_package = "infra/experimental/golangbuild/${platform}",
        cipd_version = "latest",
        cmd = ["golangbuild"],
    )

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
            **kwargs
        )

    name = builder_name(project, go_branch_short, builder_type)

    # Determine if we should be sharding tests, and how many shards.
    test_shards = 1
    capacity_constrained = is_capacity_constrained(env.low_capacity_hosts, host_type)
    if project == "go" and not capacity_constrained and go_branch_short != "go1.20":
        # TODO(mknyszek): Remove the exception for the go1.20 branch once it
        # is no longer supported.
        test_shards = 4
    if "misccompile" in run_mods:
        if project == "go":
            test_shards = 12
        else:
            test_shards = 3

    # Emit the builder definitions.
    downstream_builders = []
    if test_shards > 1 or (project == "go" and not capacity_constrained):
        downstream_builders = define_sharded_builder(env, project, name, test_shards, go_branch_short, builder_type, run_mods, base_props, base_dims, emit_builder)
    elif test_shards == 1:
        define_allmode_builder(env, name, base_props, base_dims, emit_builder)
    else:
        fail("unhandled builder definition")

    return env.bucket + "/" + name, downstream_builders

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
            if project != "go" and enabled(env.low_capacity_hosts, project, go_branch_short, builder_type)[2]
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

def define_allmode_builder(env, name, base_props, base_dims, emit_builder):
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
        service_account = env.worker_sa,
    )

luci.builder(
    name = "tricium",
    bucket = "try",
    executable = luci.recipe(
        name = "tricium_simple",
    ),
    service_account = "luci-task@golang-ci-luci.iam.gserviceaccount.com",
    dimensions = {
        "pool": "luci.golang.try",
        "os": "Linux",
        "cpu": "x86-64",
    },
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

# enabled returns three boolean values and a list or None. The first boolean value
# indicates if this builder_type should exist at all for the given project
# and branch, the second whether it should run in presubmit by default, and
# the third if it should run in postsubmit. The final list is a list of cq.location_filters
# if the builder should run in presubmit by default.
def enabled(low_capacity_hosts, project, go_branch_short, builder_type):
    pt = PROJECTS[project]
    os, arch, _, run_mods = split_builder_type(builder_type)
    host_type = host_of(builder_type)

    # Filter out new ports on old release branches.
    if os == "wasip1" and go_branch_short == "go1.20":  # GOOS=wasip1 is new to Go 1.21.
        return False, False, False, []

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
    elif project == "protobuf":
        enable_types = ["linux-amd64"]  # See issue go.dev/issue/63597.
    elif pt == PT.SPECIAL:
        fail("unhandled SPECIAL project: %s" % project)
    postsubmit = enable_types == None or any([x == "%s-%s" % (os, arch) for x in enable_types])
    presubmit = postsubmit  # Default to running in presubmit if and only if running in postsubmit.
    presubmit = presubmit and not is_capacity_constrained(low_capacity_hosts, host_type)  # Capacity.
    presubmit = presubmit and "openbsd" not in builder_type  # Not yet enabled. See CL 526255.
    if project != "go":  # Some ports run as presubmit only in the main Go repo.
        presubmit = presubmit and os not in ["js", "wasip1"]

    # Apply policies for each run mod.
    presubmit_filters = []
    for mod in run_mods:
        ex, pre, post, prefilt = RUN_MODS[mod].enabled(port_of(builder_type), project, go_branch_short)
        if not ex:
            return False, False, False, []
        presubmit = presubmit and pre
        postsubmit = postsubmit and post
        if prefilt:
            presubmit_filters.extend(prefilt)

    return True, presubmit, postsubmit, presubmit_filters

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
    postsubmit_builders_by_port = {}
    postsubmit_builders_by_project_and_branch = {}
    postsubmit_builders_with_go_repo_trigger = {}
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
                verifiers = [
                    luci.cq_tryjob_verifier(
                        builder = "try/tricium",
                        location_filters = [
                            cq.location_filter(
                                gerrit_host_regexp = "%s-review.googlesource.com" % host,
                                gerrit_project_regexp = filter_project,
                                path_regexp = ".+",
                            )
                            for host in ["go", "go-internal"]
                            for filter_project in PROJECTS
                        ],
                        mode_allowlist = [cq.MODE_ANALYZER_RUN],
                    ),
                ],
                post_actions = POST_ACTIONS,
            )

            # Define builders.
            postsubmit_builders = {}
            for builder_type in BUILDER_TYPES:
                exists, presubmit, postsubmit, presubmit_filters = enabled(LOW_CAPACITY_HOSTS, project, go_branch_short, builder_type)
                if not exists:
                    continue

                # Define presubmit builders.
                name, _ = define_builder(PUBLIC_TRY_ENV, project, go_branch_short, builder_type)
                luci.cq_tryjob_verifier(
                    builder = name,
                    cq_group = cq_group.name,
                    includable_only = not presubmit,
                    disable_reuse = True,
                    location_filters = [
                        cq.location_filter(
                            gerrit_host_regexp = "go-review.googlesource.com",
                            gerrit_project_regexp = "^%s$" % project,
                            path_regexp = filter,
                        )
                        for filter in presubmit_filters
                    ],
                )

                # For golang.org/x repos, include the ability to run presubmit
                # against all supported releases in addition to testing with tip.
                # Make presubmit mandatory for builders deemed "fast".
                # See go.dev/issue/17626.
                if project != "go" and go_branch_short != "gotip":
                    luci.cq_tryjob_verifier(
                        builder = name,
                        cq_group = go_cq_group(project, "gotip").name,
                        includable_only = builder_type != "linux-amd64",  # linux-amd64 is deemed "fast."
                        disable_reuse = True,
                    )

                # Add an x/tools builder to the Go presubmit.
                if project == "go" and builder_type == "linux-amd64":
                    luci.cq_tryjob_verifier(
                        builder = PUBLIC_TRY_ENV.bucket + "/" + builder_name("tools", go_branch_short, builder_type),
                        cq_group = cq_group.name,
                        disable_reuse = True,
                    )

                # Define post-submit builders.
                if postsubmit:
                    name, triggers = define_builder(PUBLIC_CI_ENV, project, go_branch_short, builder_type)
                    postsubmit_builders[name] = builder_type

                    # Collect all the builders that have triggers from the Go repository. Every builder needs at least one.
                    if project == "go":
                        postsubmit_builders_with_go_repo_trigger[name] = True
                        for name in triggers:
                            postsubmit_builders_with_go_repo_trigger[name] = True

            # For golang.org/x/tools, also include coverage for extra Go versions.
            if project == "tools" and go_branch_short == "gotip":
                for extra_go_release, _ in EXTRA_GO_BRANCHES.items():
                    builder_type = "linux-amd64"  # Just one fast and highly available builder is deemed enough.
                    try_builder, _ = define_builder(PUBLIC_TRY_ENV, project, extra_go_release, builder_type)
                    luci.cq_tryjob_verifier(
                        builder = try_builder,
                        cq_group = cq_group.name,
                        disable_reuse = True,
                    )
                    ci_builder, _ = define_builder(PUBLIC_CI_ENV, project, extra_go_release, builder_type)
                    postsubmit_builders[ci_builder] = builder_type

            # Collect all the postsubmit builders by port and project.
            for name, builder_type in postsubmit_builders.items():
                os, arch, _, _ = split_builder_type(builder_type)
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
                poller_branch = "master"
            luci.gitiles_poller(
                name = "%s-%s-trigger" % (project, go_branch_short),
                bucket = "ci",
                repo = "https://go.googlesource.com/%s" % project,
                refs = ["refs/heads/" + poller_branch],
                triggers = postsubmit_builders.keys(),
            )

            # Set up consoles for postsubmit builders.
            def make_console_view_entries(builders):
                return [
                    luci.console_view_entry(
                        builder = name,
                        category = display_for_builder_type(builder_type)[0],
                        short_name = display_for_builder_type(builder_type)[1],
                    )
                    for name, builder_type in builders.items()
                ]

            if project == "go":
                luci.console_view(
                    name = "%s-%s" % (project, go_branch_short),
                    repo = "https://go.googlesource.com/go",
                    title = go_branch_short,
                    refs = ["refs/heads/" + go_branch.branch],
                    entries = make_console_view_entries(postsubmit_builders),
                    header = {
                        "links": [
                            {
                                "name": "General",
                                "links": [
                                    {
                                        "text": "Contributing",
                                        "url": "https://go.dev/doc/contribute",
                                        "alt": "Go contribution guide",
                                    },
                                    {
                                        "text": "Release cycle",
                                        "url": "https://go.dev/s/release",
                                        "alt": "Go release cycle overview",
                                    },
                                    {
                                        "text": "Wiki",
                                        "url": "https://go.dev/wiki",
                                        "alt": "The Go wiki on GitHub",
                                    },
                                    {
                                        "text": "Playground",
                                        "url": "https://go.dev/play",
                                        "alt": "Go playground",
                                    },
                                ],
                            },
                        ],
                        "console_groups": [
                            {
                                "title": {"text": "golang.org/x repos on " + go_branch_short},
                                "console_ids": [
                                    # The *-by-go consoles would be more appropriate,
                                    # but because they have the same builder set and these
                                    # bubbles show just the latest build, it doesn't actually
                                    # matter.
                                    "golang/" + console_name(project, go_branch_short, "")
                                    for project in PROJECTS
                                    if project != "go"
                                ],
                            },
                        ],
                    },
                )
            else:
                console_title = project_title(project) + "-" + go_branch_short
                luci.console_view(
                    name = console_name(project, go_branch_short, ""),
                    repo = "https://go.googlesource.com/%s" % project,
                    title = console_title,
                    refs = ["refs/heads/master"],
                    entries = make_console_view_entries(postsubmit_builders),
                )
                luci.console_view(
                    name = console_name(project, go_branch_short, "-by-go"),
                    repo = "https://go.googlesource.com/go",
                    title = console_title + "-by-go-commit",
                    refs = ["refs/heads/" + go_branch.branch],
                    entries = make_console_view_entries(postsubmit_builders),
                )

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

    # Emit builder groups for each port.
    for port, builders in postsubmit_builders_by_port.items():
        luci.list_view(
            # Put "z" at the beginning to sort this at the bottom of the page.
            name = "z-port-%s" % port.replace("/", "-"),
            title = "all-%s" % port,
            entries = builders,
        )

def _define_go_internal_ci():
    for go_branch_short, go_branch in GO_BRANCHES.items():
        # TODO(yifany): Simplify cq.location_filter once Tricium to CV
        # migration (go/luci/tricium) is done.
        cq_group_name = ("go-internal_%s" % go_branch_short).replace(".", "-")
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
                repo = "https://go-internal.googlesource.com/go",
                refs = ["refs/heads/%s" % go_branch.branch],
            ),
            allow_submit_with_open_deps = True,
            trust_dry_runner_deps = True,
            verifiers = [
                luci.cq_tryjob_verifier(
                    builder = "try/tricium",
                    location_filters = [
                        cq.location_filter(
                            gerrit_host_regexp = "%s-review.googlesource.com" % host,
                            gerrit_project_regexp = filter_project,
                            path_regexp = ".+",
                        )
                        for host in ["go", "go-internal"]
                        for filter_project in PROJECTS
                    ],
                    mode_allowlist = [cq.MODE_ANALYZER_RUN],
                ),
            ],
            # TODO(prattmic): Set post_actions to apply TryBot-Result labels.
        )

        for builder_type in BUILDER_TYPES:
            exists, _, _, _ = enabled(LOW_CAPACITY_HOSTS, "go", go_branch_short, builder_type)

            # The internal host only has access to some machines. As of
            # writing, that means all the GCE-hosted (high-capacity) builders
            # and that's it.
            exists = exists and not is_capacity_constrained(LOW_CAPACITY_HOSTS, host_of(builder_type))
            if not exists:
                continue

            # Define presubmit builders. Since there's no postsubmit to monitor,
            # all possible builders are required.
            name, _ = define_builder(SECURITY_TRY_ENV, "go", go_branch_short, builder_type)
            luci.cq_tryjob_verifier(
                builder = name,
                cq_group = cq_group_name,
                disable_reuse = True,
            )

_define_go_ci()
_define_go_internal_ci()

exec("./recipes.star")
