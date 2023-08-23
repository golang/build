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
    ],
    acls = [
        acl.entry(
            roles = acl.PROJECT_CONFIGS_READER,
            groups = "all",
        ),
        acl.entry(
            roles = acl.CQ_COMMITTER,
            groups = "mdb/golang-luci-admin",
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
    luci.bucket(name = "ci"),
]

SECURITY_REALMS = [
    luci.realm(name = "pools/security-try"),
    luci.realm(name = "pools/security-try-workers"),
    luci.bucket(name = "security-try"),
]

luci.realm(name = "pools/prod")

# Allow everyone to see public builds, pools, bots, and tasks.
# WARNING: this doesn't do much for Swarming entities -- chromium-swarm
# has a global allow-all ACL that supersedes us. Private realms run in
# chrome-swarming.
luci.binding(
    roles = ["role/swarming.poolViewer", "role/buildbucket.reader"],
    realm = PUBLIC_REALMS,
    groups = "all",
)

# Allow external contributors to trigger public builds.
luci.binding(
    roles = ["role/buildbucket.triggerer"],
    realm = PUBLIC_REALMS,
    groups = ["project-golang-may-start-trybots", "project-golang-approvers", "mdb/golang-team"],
)

# Allow security release participants to see and trigger security builds, etc.
# WARNING: similar to above, chrome-swarming is open to all Googlers.
luci.binding(
    roles = ["role/swarming.poolViewer", "role/buildbucket.reader", "role/buildbucket.triggerer"],
    realm = SECURITY_REALMS,
    groups = ["mdb/golang-security-policy", "mdb/golang-release-eng-policy"],
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
        coordinator_sa = coordinator_sa + "@golang-ci-luci.iam.gserviceaccount.com",
        coordinator_pool = "luci.golang.%s" % bucket,
        worker_sa = worker_sa + "@golang-ci-luci.iam.gserviceaccount.com",
        worker_pool = "luci.golang.%s-workers" % bucket,
        shared_worker_pool = "luci.golang.shared-workers",
        low_capacity_hosts = low_capacity,
    )

# LOW_CAPACITY_HOSTS lists "hosts" that have fixed, relatively low capacity.
# They need to match the builder type, excluding any run mods.
LOW_CAPACITY_HOSTS = [
    "darwin-amd64",
    "linux-ppc64le",
]

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
    name = "prod-builders",
    title = "Production builders",
)

# BUILDER_TYPES lists possible builder types.
#
# A builder type is a combination of a GOOS and GOARCH, an optional suffix that
# specifies the OS version, and a series of run-time modifications
# (listed in RUN_MODS).
#
# The format of a builder type is thus $GOOS-$GOARCH(_osversion)?(-$RUN_MOD)*.
BUILDER_TYPES = [
    "darwin-amd64",
    "linux-386",
    "linux-386-longtest",
    "linux-amd64",
    "linux-amd64-boringcrypto",
    "linux-amd64-longtest",
    "linux-amd64-longtest-race",
    "linux-amd64-race",
    "linux-amd64-misccompile",
    "linux-arm64",
    "linux-ppc64le",
    "windows-386",
    "windows-amd64",
    "windows-amd64-longtest",
    "windows-amd64-race",
]

# NO_NETWORK_BUILDERS are a subset of builder types
# where we require the no-network check to run.
NO_NETWORK_BUILDERS = [
    "linux-386",
    "linux-amd64",
]

# RUN_MODS is a list of valid run-time modifications to the way we
# build and test our various projects.
RUN_MODS = [
    "longtest",
    "race",
    "boringcrypto",
    "misccompile",
]

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
    "exp": PT.LIBRARY,
    "image": PT.LIBRARY,
    "mobile": PT.SPECIAL,
    "mod": PT.CORE,
    "net": PT.CORE,
    "oauth2": PT.LIBRARY,
    "perf": PT.TOOL,
    "pkgsite": PT.TOOL,
    "pkgsite-metrics": PT.TOOL,
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

# GO_BRANCHES lists the branches of the "go" project to build and test against.
# Keys in this map are shortened aliases while values are the git branch name.
GO_BRANCHES = {
    "gotip": struct(branch = "master", bootstrap = "1.20.6"),
    "go1.21": struct(branch = "release-branch.go1.21", bootstrap = "1.17.13"),
    "go1.20": struct(branch = "release-branch.go1.20", bootstrap = "1.17.13"),
}

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

def dimensions_of(low_capacity_hosts, builder_type):
    """dimensions_of returns the bot dimensions for a builder type."""
    os, arch, suffix, _ = split_builder_type(builder_type)

    # TODO(mknyszek): Consider adding "_suffix " to the end of this.
    host = "%s-%s" % (os, arch)

    # LUCI uses Mac to refer to macOS.
    os = os.replace("darwin", "mac")

    # Capitalize the OS. LUCI OS dimensions are always capitalized.
    os = os.capitalize()

    # We run 386 builds on AMD64.
    arch = arch.replace("386", "amd64")

    # LUCI calls amd64 x86-64.
    arch = arch.replace("amd64", "x86-64")

    if suffix != "":
        # Narrow down the dimensions using the suffix.
        if os == "Linux":
            # linux-amd64_debian11 -> Debian-11
            os = suffix.replace("debian", "Debian-")
        elif os == "Mac":
            # darwin-amd64_12.6 -> Mac-12.6
            os = "Mac-" + suffix
    else:
        # Set the default dimensions for each platform, but only
        # if it's not a low-capacity host. Low-capacity hosts
        # need their dimensions to be as general as possible.
        #
        # Even without a suffix, we need to narrow down the
        # dimensions so robocrop can correctly identify the
        # queue length. This *must* line up with the
        # expected_dimensions field for the botset in the
        # internal config: //starlark/common/envs/golang.star
        if os == "Linux" and host not in low_capacity_hosts:
            os = "Debian-11"
        elif os == "Mac":
            os = "Mac-12.6"
        elif os == "Windows":
            os = "Windows-10"

        # TODO: Add more platforms and decide on whether we
        # even want this concept of a "default", suffixless
        # builder.

    return {"os": os, "cpu": arch}

def is_capacity_constrained(low_capacity_hosts, builder_type):
    dims = dimensions_of(low_capacity_hosts, builder_type)
    return any([dimensions_of(low_capacity_hosts, x) == dims for x in low_capacity_hosts])

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

    # Add an x_ prefix to the project to help make it clear that
    # we're testing a golang.org/x/* repository. These repositories
    # do not have an "internal" counterpart.
    return "x_%s-%s-%s" % (project, go_branch_short, builder_type)

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
        go_branch_short: A go repository branch name defined in `GO_BRANCHES`.
        builder_type: A name defined in `BUILDER_TYPES`.

    Returns:
        The full name including a bucket prefix.
    """

    # Contruct the basic properties that will apply to all builders for
    # this combination.
    base_props = {
        "project": project,
        # NOTE: LUCI will pass in the commit information. This is
        # extra information that's only necessary for x/ repos.
        # However, we pass it to all builds for consistency and
        # convenience.
        "go_branch": GO_BRANCHES[go_branch_short].branch,
        "bootstrap_version": GO_BRANCHES[go_branch_short].bootstrap,
        "env": {},
    }

    # On less-supported platforms, we may not have bootstraps before 1.21
    # started cross-compiling everything.
    if "ppc64le" in builder_type and (base_props["bootstrap_version"].startswith("1.20") or base_props["bootstrap_version"].startswith("1.1")):
        base_props["bootstrap_version"] = "1.21.0"

    os, arch, _, run_mods = split_builder_type(builder_type)

    # We run 386 builds on amd64 with GO[HOST]ARCH set.
    if arch == "386":
        base_props["env"]["GOARCH"] = "386"
        base_props["env"]["GOHOSTARCH"] = "386"

    # Construct the basic dimensions for the build/test running part of the build.
    #
    # Note that these should generally live in the worker pools.
    base_dims = dimensions_of(env.low_capacity_hosts, builder_type)
    base_dims["pool"] = env.worker_pool
    if is_capacity_constrained(env.low_capacity_hosts, builder_type):
        # Scarce resources live in the shared-workers pool.
        base_dims["pool"] = env.shared_worker_pool

    # TODO(heschi): Select the version based on the macOS version or builder type
    if os == "darwin":
        base_props["xcode_version"] = "12e5244e"

    # Turn on the no-network check.
    if builder_type in NO_NETWORK_BUILDERS:
        base_props["no_network"] = True

        # Leave release branches out of scope, they can't work until some
        # test fixes are backported, but doing that might not be worth it.
        # TODO(dmitshur): Delete this after Go 1.21 drops off.
        if project == "go" and (go_branch_short == "go1.21" or go_branch_short == "go1.20"):
            base_props.pop("no_network")

    if "longtest" in run_mods:
        base_props["long_test"] = True
        base_props["env"]["GO_TEST_TIMEOUT_SCALE"] = "5"
    if "race" in run_mods:
        base_props["race_mode"] = True
    if "boringcrypto" in run_mods:
        base_props["env"]["GOEXPERIMENT"] = "boringcrypto"
    if "misccompile" in run_mods:
        # The misccompile mod indicates that the builder should act as a "misc-compile" builder,
        # that is to cross-compile all non-first-class ports to quickly flag portability issues.
        base_props["compile_only"] = True
        base_props["misc_ports"] = True

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
        "golang.parallel_compile_only_ports": 50,  # Try both and gather data.
        "golang.parallel_compile_only_ports_maxprocs": 50,  # Try both and gather data.
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
    def emit_builder(name, dimensions, properties, service_account, **kwargs):
        exps = dict(experiments)
        if "ppc64le" in dimensions["cpu"]:
            exps["luci.best_effort_platform"] = 100
        luci.builder(
            name = name,
            bucket = env.bucket,
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

    # Emit the builder definitions.
    if project == "go":
        define_go_builder(env, name, go_branch_short, builder_type, run_mods, base_props, base_dims, emit_builder)
    else:
        define_subrepo_builder(env, name, base_props, base_dims, emit_builder)

    return env.bucket + "/" + name

def define_go_builder(env, name, go_branch_short, builder_type, run_mods, base_props, base_dims, emit_builder):
    os, arch, _, _ = split_builder_type(builder_type)

    # Create 3 builders: the main entrypoint/coordinator builder,
    # a builder just to run make.bash, and a builder to run tests.
    #
    # This separation of builders allows for the coordinator builder to have elevated privileges,
    # because it will never run code still in code review. Furthermore, this separation allows
    # for sharding tests, which is very important for build latency for the Go repository
    # specifically.
    #
    # The main/coordinator builder must be the only one with the ability
    # to schedule new builds.
    coord_name = name
    build_name = name + "-build_go"
    test_name = name + "-test_only"

    # Determine if we should be sharding tests, and how many shards.
    test_shards = 1
    if not is_capacity_constrained(env.low_capacity_hosts, builder_type) and go_branch_short != "go1.20":
        # TODO(mknyszek): Remove the exception for the go1.20 branch once it
        # is no longer supported.
        test_shards = 4
    if "misccompile" in run_mods:
        test_shards = 12

    # The main repo builder also triggers subrepo builders of the same builder type.
    #
    # TODO(mknyszek): This rule will not apply for some ports in the future. Some
    # ports only apply to the main Go repository and are not supported by all subrepos.
    # PROJECTS should probably contain a table of supported ports or something.
    builders_to_trigger = []
    if env.bucket == "ci":
        builders_to_trigger = [
            "golang/%s/%s" % (env.bucket, builder_name(project, go_branch_short, builder_type))
            for project in PROJECTS
            if project != "go" and enabled(env.low_capacity_hosts, project, go_branch_short, builder_type)[1]
        ]

    # Coordinator builder.
    #
    # Specify the dimensions directly. Coordinator builds can have very vague dimensions
    # because we don't really care what machines they run on. We still need to specify
    # something for robocrop, but the robocrop config will look for just "Linux."
    coord_dims = {
        "pool": env.coordinator_pool,
        "os": "Linux",
        "cpu": "x86-64",
    }
    coord_props = dict(base_props)
    coord_props.update({
        "mode": GOLANGBUILD_MODES["COORDINATOR"],
        "coord_mode": {
            "build_builder": "golang/" + env.bucket + "/" + build_name,
            "test_builder": "golang/" + env.bucket + "/" + test_name,
            "num_test_shards": test_shards,
            "builders_to_trigger_after_toolchain_build": builders_to_trigger,
            "target_goos": os,
            "target_goarch": arch,
        },
    })
    emit_builder(
        name = coord_name,
        dimensions = coord_dims,
        properties = coord_props,
        service_account = env.coordinator_sa,
    )

    # Build builder.
    build_dims = dict(base_dims)
    build_props = dict(base_props)
    build_props.update({
        "mode": GOLANGBUILD_MODES["BUILD"],
        "build_mode": {},
    })
    emit_builder(
        name = build_name,
        dimensions = build_dims,
        properties = build_props,
        service_account = env.worker_sa,
    )

    # Test builder.
    test_dims = dict(base_dims)
    test_props = dict(base_props)
    test_props.update({
        "mode": GOLANGBUILD_MODES["TEST"],
        "test_mode": {},
        # The default is no sharding. This may be overwritten by the coordinator builder.
        "test_shard": {"shard_id": 0, "num_shards": 1},
    })
    emit_builder(
        name = test_name,
        dimensions = test_dims,
        properties = test_props,
        service_account = env.worker_sa,
        allowed_property_overrides = ["test_shard"],
    )

def define_subrepo_builder(env, name, base_props, base_dims, emit_builder):
    # Create an "ALL" mode builder which just performs the full build serially.
    #
    # This builder is substantially simpler than the Go builders because it doesn't need
    # to trigger any downstream builders, and also doesn't need test sharding (all subrepos'
    # tests run fast enough that it's unnecessary).
    all_props = dict(base_props)
    all_props.update({
        "mode": GOLANGBUILD_MODES["ALL"],
    })
    emit_builder(
        name = name,
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

# enabled returns two boolean values: the first one indicates if this builder_type
# should run in presubmit for the given project and branch, and the second indicates
# if this builder_type should run in postsubmit for the given project and branch.
# buildifier: disable=unused-variable
def enabled(low_capacity_hosts, project, go_branch_short, builder_type):
    pt = PROJECTS[project]
    os, arch, _, run_mods = split_builder_type(builder_type)

    enable_types = None
    if pt == PT.TOOL:
        enable_types = ["linux-amd64", "windows-amd64", "darwin-amd64"]
    elif project == "mobile":
        enable_types = ["linux-amd64", "android"]
    elif pt == PT.SPECIAL:
        fail("unhandled SPECIAL project: %s" % project)
    postsubmit = enable_types == None or any([x == "%s-%s" % (os, arch) for x in enable_types])
    presubmit = postsubmit \
        and not any([x in run_mods for x in ["longtest", "race"]]) \
        and not is_capacity_constrained(low_capacity_hosts, builder_type)
    return presubmit, postsubmit

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
    for project in PROJECTS:
        for go_branch_short, go_branch in GO_BRANCHES.items():
            # Set up a CQ group for the builder definitions below.
            #
            # cq group names must match "^[a-zA-Z][a-zA-Z0-9_-]{0,39}$"
            cq_group_name = ("%s_repo_%s" % (project, go_branch_short)).replace(".", "-")
            luci.cq_group(
                name = cq_group_name,
                acls = [acl.entry(
                    roles = acl.CQ_DRY_RUNNER,
                    groups = ["project-golang-may-start-trybots", "project-golang-approvers", "mdb/golang-team"],
                )],
                watch = cq.refset(
                    repo = "https://go.googlesource.com/%s" % project,
                    refs = [
                        "refs/heads/%s" % go_branch.branch
                    ]
                ),
                allow_submit_with_open_deps = True,
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
                presubmit, postsubmit = enabled(LOW_CAPACITY_HOSTS, project, go_branch_short, builder_type)

                # Define presubmit builders.
                name = define_builder(PUBLIC_TRY_ENV, project, go_branch_short, builder_type)
                luci.cq_tryjob_verifier(
                    builder = name,
                    cq_group = cq_group_name,
                    includable_only = not presubmit,
                )

                # Define post-submit builders.
                if postsubmit:
                    name = define_builder(PUBLIC_CI_ENV, project, go_branch_short, builder_type)
                    postsubmit_builders[name] = display_for_builder_type(builder_type)

            # Create the gitiles_poller last because we need the full set of builders to
            # trigger at the point of definition.
            #
            # N.B. A gitiles poller is only necessary for the subrepo itself.
            # Builds against go on different branches will be triggered by the
            # corresponding main Go repo builders (e.g. go1.20-linux-amd64 will
            # trigger x_build-go1.20-linux-amd64 against the same go1.20 branch commit).
            # This is controlled by the "builders_to_trigger" property on those
            # builders.
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
                        category = display[0],
                        short_name = display[1],
                    )
                    for name, display in builders.items()
                ]

            if project == "go":
                luci.console_view(
                    name = "%s-%s-ci" % (project, go_branch_short),
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
                                    # The *-by-go-ci consoles would be more appropriate,
                                    # but because they have the same builder set and these
                                    # bubbles show just the latest build, it doesn't actually
                                    # matter.
                                    "golang/%s-%s-ci" % (project, go_branch_short)
                                    for project in PROJECTS
                                    if project != "go"
                                ],
                            },
                        ],
                    },
                )
            else:
                console_title = "x/" + project
                if go_branch_short != "gotip":
                    console_title += " on " + go_branch_short
                luci.console_view(
                    name = "%s-%s-ci" % (project, go_branch_short),
                    repo = "https://go.googlesource.com/%s" % project,
                    title = console_title,
                    refs = ["refs/heads/master"],
                    entries = make_console_view_entries(postsubmit_builders),
                )
                luci.console_view(
                    name = "%s-%s-by-go-ci" % (project, go_branch_short),
                    repo = "https://go.googlesource.com/go",
                    title = console_title + " (by go commit)",
                    refs = ["refs/heads/" + go_branch.branch],
                    entries = make_console_view_entries(postsubmit_builders),
                )

def _define_go_internal_ci():
    for go_branch_short, go_branch in GO_BRANCHES.items():
        # TODO(yifany): Simplify cq.location_filter once Tricium to CV
        # migration (go/luci/tricium) is done.
        cq_group_name = ("go-internal_%s" % go_branch_short).replace(".", "-")
        luci.cq_group(
            name = cq_group_name,
            acls = [acl.entry(
                roles = acl.CQ_DRY_RUNNER,
                groups = ["mdb/golang-security-policy", "mdb/golang-release-eng-policy"],
            )],
            watch = cq.refset(
                repo = "https://go-internal.googlesource.com/go",
                refs = ["refs/heads/%s" % go_branch.branch],
            ),
            allow_submit_with_open_deps = True,
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
            presubmit, _ = enabled(LOW_CAPACITY_HOSTS, "go", go_branch_short, builder_type)

            # Define presubmit builders.
            name = define_builder(SECURITY_TRY_ENV, "go", go_branch_short, builder_type)
            luci.cq_tryjob_verifier(
                builder = name,
                cq_group = cq_group_name,
                includable_only = not presubmit,
            )

            # TODO(yifany): Define postsubmit builders if needed.

_define_go_ci()
_define_go_internal_ci()

exec("./recipes.star")
