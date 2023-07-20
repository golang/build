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
                "role/swarming.taskTriggerer",
                # Allow owners to trigger and cancel LUCI Scheduler jobs.
                "role/scheduler.owner",
                # Allow owners to trigger and cancel any build.
                "role/buildbucket.owner",
                # Allow owners to create/edit ResultDB invocations (for local result_adapter development).
                # TODO(dmitshur): Remove or move to AOD after it stops being actively used.
                "role/resultdb.invocationCreator",
            ],
            groups = "mdb/golang-luci-admin",
        ),

        # Allow any googler to see all bots and tasks there.
        luci.binding(
            roles = "role/swarming.poolViewer",
            groups = "googlers",
        ),

        # Allow any googler to read/validate/reimport the project configs.
        luci.binding(
            roles = "role/configs.developer",
            groups = "googlers",
        ),

        # Allow everyone in the world to see bulders.
        luci.binding(
            roles = "role/buildbucket.reader",
            groups = "all",
        ),

        # Allow task service accounts to spawn builds.
        luci.binding(
            roles = "role/buildbucket.triggerer",
            groups = "project-golang-task-accounts",
            users = "tricium-prod@appspot.gserviceaccount.com",
        ),

        # Allow external contributions to run try jobs.
        luci.binding(
            roles = "role/cq.dryRunner",
            groups = ["project-golang-may-start-trybots", "project-golang-approvers", "mdb/golang-team"],
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
        acl.entry(
            roles = acl.CQ_DRY_RUNNER,
            groups = ["project-golang-may-start-trybots", "project-golang-approvers", "mdb/golang-team"],
        ),
    ],
)

# Per-service tweaks.
luci.logdog(gs_bucket = "logdog-golang-archive")

# Realms with ACLs for corresponding Swarming pools.
luci.realm(name = "pools/ci")
luci.realm(name = "pools/ci-workers")
luci.realm(name = "pools/try")
luci.realm(name = "pools/try-workers")
luci.realm(name = "pools/shared-workers")
luci.realm(name = "pools/prod")

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

# The try bucket will include builders which work on pre-commit or pre-review
# code.
luci.bucket(name = "try")

# The ci bucket will include builders which work on post-commit code.
luci.bucket(name = "ci")

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
# A builder type is a combination of a host and a series of run-time
# modifications, listed in RUN_MODS.
#
# The format of a builder type is thus $HOST(-$RUN_MOD)*.
BUILDER_TYPES = [
    "linux-386",
    "linux-386-longtest",
    "linux-amd64",
    "linux-amd64-boringcrypto",
    "linux-amd64-longtest",
    "linux-amd64-longtest-race",
    "linux-amd64-race",
    "linux-arm64",
    "windows-amd64",
    "windows-amd64-longtest",
    "windows-amd64-race",
    "darwin-amd64",
]

# RUN_MODS is a list of valid run-time modifications to the way we
# build and test our various projects.
RUN_MODS = [
    "longtest",
    "race",
    "boringcrypto",
]

# PROJECTS lists the go.googlesource.com/<project> projects we build and test for.
#
# TODO(mknyszek): This likely needs some form of classification.
PROJECTS = [
    "go",
    "arch",
    "benchmarks",
    "build",
    "crypto",
    "debug",
    "exp",
    "image",
    "mobile",
    "mod",
    "net",
    "oauth2",
    "perf",
    "pkgsite",
    "pkgsite-metrics",
    "review",
    "sync",
    "sys",
    "telemetry",
    "term",
    "text",
    "time",
    "tools",
    "vuln",
    "vulndb",
    "website",
]

# GO_BRANCHES lists the branches of the "go" project to build and test against.
# Keys in this map are shortened aliases while values are the git branch name.
GO_BRANCHES = {
    "gotip": struct(branch="master", bootstrap="go1.20.6"),
    "go1.20": struct(branch="release-branch.go1.20", bootstrap="go1.17.6"),
}

# HOSTS is a mapping of host types to Swarming dimensions.
#
# The format of each host is $GOOS-$GOARCH(-$HOST_SPECIFIER)?.
HOSTS = {
    "linux-amd64": {"os": "Linux", "cpu": "x86-64"},
    "linux-arm64": {"os": "Linux", "cpu": "arm64"},
    "windows-amd64": {"os": "Windows", "cpu": "x86-64"},
    "darwin-amd64": {"os": "Mac", "cpu": "x86-64"},
}

# Return the host type for the given builder type.
def host_of(builder_type):
    return "-".join(builder_type.split("-")[:2])

# Return a list of run-time modifications enabled in the given builder type.
def run_mods_of(builder_type):
    return [x for x in builder_type.split("-") if x in RUN_MODS]

# builder_name produces the final builder name.
def builder_name(project, go_branch_short, builder_type):
    if project == "go":
        # Omit the project name for the main Go repository.
        # The branch short name already has a "go" prefix so
        # it's clear what the builder is building and testing.
        return "%s-%s" % (go_branch_short, builder_type)

    # Add an x_ prefix to the project to help make it clear that
    # we're testing a golang.org/x/* repository.
    return "x_%s-%s-%s" % (project, go_branch_short, builder_type)

# Enum values for golangbuild's "mode" property.
GOLANGBUILD_MODES = {
    "ALL": 0,
    "COORDINATOR": 1,
    "BUILD": 2,
    "TEST": 3,
}

def define_builder(bucket, project, go_branch_short, builder_type):
    """Creates a builder definition.

    Args:
        bucket: A LUCI bucket name, e.g. "ci".
        project: A go project defined in `PROJECTS`.
        go_branch_short: A go repository branch name defined in `GO_BRANCHES`.
        builder_type: A name defined in `BUILDER_TYPES`.

    Returns:
        The full name including a bucket prefix.
    """

    properties = {
        "project": project,
        # NOTE: LUCI will pass in the commit information. This is
        # extra information that's only necessary for x/ repos.
        # However, we pass it to all builds for consistency and
        # convenience.
        "go_branch": GO_BRANCHES[go_branch_short].branch,
        "bootstrap_version": GO_BRANCHES[go_branch_short].bootstrap,
        "env": {},
    }

    # We run 386 builds on amd64 with GO[HOST]ARCH set.
    host = host_of(builder_type)
    if builder_type.split("-")[1] == "386":
        host = host.replace("386", "amd64")
        properties["env"]["GOARCH"] = "386"
        properties["env"]["GOHOSTARCH"] = "386"

    # Copy the dimensions and set the pool, which aligns with the bucket names.
    dimensions = dict(HOSTS[host])
    dimensions["pool"] = "luci.golang." + bucket
    if dimensions["os"] == "Mac":
        # Macs are currently relatively scarce, so they live in the shared-workers pool.
        dimensions["pool"] = "luci.golang.shared-workers"

    name = builder_name(project, go_branch_short, builder_type)

    # TODO(heschi): Select the version based on the macOS version or builder type
    if dimensions["os"] == "Mac":
        properties["xcode_version"] = "12e5244e"

    run_mods = run_mods_of(builder_type)
    if "longtest" in run_mods:
        properties["long_test"] = True
        properties["env"]["GO_TEST_TIMEOUT_SCALE"] = "5"
    if "race" in run_mods:
        properties["race_mode"] = True
    if "boringcrypto" in run_mods:
        properties["env"]["GOEXPERIMENT"] = "boringcrypto"
    if project == "go" and bucket == "ci":
        # The main repo builder also triggers subrepo builders of the same builder type.
        #
        # TODO(mknyszek): This rule will not apply for some ports in the future. Some
        # ports only apply to the main Go repository and are not supported by all subrepos.
        # PROJECTS should probably contain a table of supported ports or something.
        properties["builders_to_trigger"] = [
            "golang/%s/%s" % (bucket, builder_name(project, go_branch_short, builder_type))
            for project in PROJECTS
            if project != "go"
        ]

    # Named cache for cipd tools root, required for golang.cache_tools_roots.
    tools_cache = "tools"
    caches = [swarming.cache(tools_cache)]
    properties["tools_cache"] = tools_cache

    # Determine which experiments to apply.
    experiments = {
        "golang.cache_tools_root": 100,
        "golang.no_network_in_short_test_mode": 100,
    }

    # TODO(dmitshur): Make no-network work for the main Go repo. See https://ci.chromium.org/b/8776274213581709009.
    if project == "go":
        experiments.pop("golang.no_network_in_short_test_mode")

    # Construct the executable reference.
    executable = luci.executable(
        name = "golangbuild",
        cipd_package = "infra/experimental/golangbuild/${platform}",
        cipd_version = "latest",
        cmd = ["golangbuild"],
    )

    luci.builder(
        name = name,
        bucket = bucket,
        executable = executable,
        dimensions = dimensions,
        properties = properties,
        service_account = "luci-task@golang-ci-luci.iam.gserviceaccount.com",
        resultdb_settings = resultdb.settings(
            enable = True,
        ),
        caches = caches,
        experiments = experiments,
    )

    # Create experimental builders for sharding tests, but only for a limited
    # set of configurations.
    if project == "go" and go_branch_short == "gotip" and \
       (host_of(builder_type) == "linux-amd64" or host_of(builder_type) == "windows-amd64"):
        coord_name = name + "-coordinator"
        build_name = name + "-build_go"
        test_name = name + "-test_only"

        # Coordinator builder.
        coord_dims = dict(HOSTS["linux-amd64"])
        coord_dims.update({
            "pool": "luci.golang." + bucket,
        })
        coord_props = dict(properties)
        coord_props.update({
            "mode": GOLANGBUILD_MODES["COORDINATOR"],
            "coord_mode": {
                "build_builder": "golang/" + bucket + "/" + build_name,
                "test_builder": "golang/" + bucket + "/" + test_name,
                "num_test_shards": 2,
            },
            "builders_to_trigger": [],  # Don't try to trigger anything for now.
        })
        luci.builder(
            name = coord_name,
            bucket = bucket,
            executable = executable,
            dimensions = coord_dims,
            properties = coord_props,
            service_account = "luci-task@golang-ci-luci.iam.gserviceaccount.com",
            resultdb_settings = resultdb.settings(
                enable = True,
            ),
            caches = caches,
            experiments = experiments,
        )

        # Build builder.
        build_dims = dict(dimensions)
        build_dims.update({
            "pool": "luci.golang." + bucket + "-workers",
        })
        build_props = dict(properties)
        build_props.update({
            "mode": GOLANGBUILD_MODES["BUILD"],
            "build_mode": {},
            "builders_to_trigger": [],  # Don't try to trigger anything.
        })
        luci.builder(
            name = build_name,
            bucket = bucket,
            executable = executable,
            dimensions = build_dims,
            properties = build_props,
            service_account = "luci-task@golang-ci-luci.iam.gserviceaccount.com",
            resultdb_settings = resultdb.settings(
                enable = True,
            ),
            caches = caches,
            experiments = experiments,
        )

        # Test builder.
        test_dims = dict(dimensions)
        test_dims.update({
            "pool": "luci.golang." + bucket + "-workers",
        })
        test_props = dict(properties)
        test_props.update({
            "mode": GOLANGBUILD_MODES["TEST"],
            "test_mode": {},
            "test_shard": {},
            "builders_to_trigger": [],  # Don't try to trigger anything.
        })
        luci.builder(
            name = test_name,
            bucket = bucket,
            executable = executable,
            dimensions = test_dims,
            properties = test_props,
            allowed_property_overrides = ["test_shard"],
            service_account = "luci-task@golang-ci-luci.iam.gserviceaccount.com",
            resultdb_settings = resultdb.settings(
                enable = True,
            ),
            caches = caches,
            experiments = experiments,
        )

    return bucket + "/" + name

luci.builder(
    name = "tricium",
    bucket = "try",
    executable = luci.recipe(
        name = "tricium_simple",
    ),
    service_account = "luci-task@golang-ci-luci.iam.gserviceaccount.com",
    dimensions = HOSTS[host_of("linux-amd64")],
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
def enabled(project, go_branch_short, builder_type):
    run_mods = run_mods_of(builder_type)
    presubmit = not any(["longtest" in run_mods, "race" in run_mods])
    postsubmit = True
    return presubmit, postsubmit

def _define_go_ci():
    for project in PROJECTS:
        for go_branch_short, branch_props in GO_BRANCHES.items():
            # Set up a CQ group for the builder definitions below.
            #
            # cq group names must match "^[a-zA-Z][a-zA-Z0-9_-]{0,39}$"
            cq_group_name = ("%s_repo_%s" % (project, go_branch_short)).replace(".", "-")
            luci.cq_group(
                name = cq_group_name,
                watch = cq.refset(
                    repo = "https://go.googlesource.com/%s" % project,
                    refs = ["refs/heads/%s" % branch_props.branch],
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
            )

            # Define builders.
            postsubmit_builders = {}
            for builder_type in BUILDER_TYPES:
                presubmit, postsubmit = enabled(project, go_branch_short, builder_type)

                # Define presubmit builders.
                name = define_builder("try", project, go_branch_short, builder_type)
                luci.cq_tryjob_verifier(
                    builder = name,
                    cq_group = cq_group_name,
                    includable_only = not presubmit,
                )

                # Define post-submit builders.
                if postsubmit:
                    name = define_builder("ci", project, go_branch_short, builder_type)
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
                poller_branch = branch_props.branch
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
                    refs = ["refs/heads/" + branch_props.branch],
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
                    refs = ["refs/heads/" + branch_props.branch],
                    entries = make_console_view_entries(postsubmit_builders),
                )

def _define_go_internal_ci():
    for go_branch_short, branch_props in GO_BRANCHES.items():
        # TODO(yifany): Simplify cq.location_filter once Tricium to CV
        # migration (go/luci/tricium) is done.
        cq_group_name = ("go-internal_%s" % go_branch_short).replace(".", "-")
        luci.cq_group(
            name = cq_group_name,
            watch = cq.refset(
                repo = "https://go-internal.googlesource.com/go",
                refs = ["refs/heads/%s" % branch_props.branch],
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
        )

        for builder_type in BUILDER_TYPES:
            presubmit, _ = enabled("go", go_branch_short, builder_type)

            # Define presubmit builders.
            name = define_builder("try", "go-internal", go_branch_short, builder_type)
            luci.cq_tryjob_verifier(
                builder = name,
                cq_group = cq_group_name,
                includable_only = not presubmit,
            )

            # TODO(yifany): Define postsubmit builders if needed.

_define_go_ci()
_define_go_internal_ci()

exec("./recipes.star")
