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
            groups = "project-golang-may-start-trybots",
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
            groups = "project-golang-may-start-trybots",
        ),
    ],
)

# Per-service tweaks.
luci.logdog(gs_bucket = "logdog-golang-archive")

# Realms with ACLs for corresponding Swarming pools.
luci.realm(name = "pools/ci")
luci.realm(name = "pools/try")
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
    "linux-amd64",
    "linux-amd64-longtest",
    "linux-amd64-longtest-race",
    "linux-amd64-race",
    "windows-amd64",
    "windows-amd64-longtest",
    "windows-amd64-race",
]

# RUN_MODS is a list of valid run-time modifications to the way we
# build and test our various projects.
RUN_MODS = [
    "longtest",
    "race",
]

# PROJECTS lists the go.googlesource.com/<project> projects we build and test for.
#
# TODO(mknyszek): This likely needs some form of classification.
PROJECTS = [
    "go",
    "build",
    "image",
    "mod",
    "tools",
]

# GO_BRANCHES lists the branches of the "go" project to build and test against.
# Keys in this map are shortened aliases while values are the git branch name.
GO_BRANCHES = {
    "gotip": "master",
    "go1.20": "release-branch.go1.20",
}

# HOSTS is a mapping of host types to Swarming dimensions.
#
# The format of each host is $GOOS-$GOARCH(-$HOST_SPECIFIER)?.
HOSTS = {
    "linux-amd64": {"os": "Linux", "cpu": "x86-64"},
    "windows-amd64": {"os": "Windows", "cpu": "x86-64"},
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
    dimensions = HOSTS[host_of(builder_type)]
    name = builder_name(project, go_branch_short, builder_type)
    properties = {
        "project": project,
        # NOTE: LUCI will pass in the commit information. This is
        # extra information that's only necessary for x/ repos.
        # However, we pass it to all builds for consistency and
        # convenience.
        "go_branch": GO_BRANCHES[go_branch_short],
    }

    run_mods = run_mods_of(builder_type)
    if "race" in run_mods:
        properties["race_mode"] = True
    if "longtest" in run_mods:
        properties["env"] = {
            "GO_TEST_SHORT": "0",
            "GO_TEST_TIMEOUT_SCALE": "5",
        }
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

    luci.builder(
        name = name,
        bucket = bucket,
        executable = luci.executable(
            name = "golangbuild",
            cipd_package = "infra/experimental/golangbuild/${platform}",
            cipd_version = "latest",
            cmd = ["golangbuild"],
        ),
        dimensions = dimensions,
        properties = properties,
        service_account = "luci-task@golang-ci-luci.iam.gserviceaccount.com",
        resultdb_settings = resultdb.settings(
            enable = True,
        ),
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
        for go_branch_short, go_branch in GO_BRANCHES.items():
            # Set up a CQ group for the builder definitions below.
            #
            # cq group names must match "^[a-zA-Z][a-zA-Z0-9_-]{0,39}$"
            cq_group_name = ("%s_repo_%s" % (project, go_branch_short)).replace(".", "-")
            luci.cq_group(
                name = cq_group_name,
                watch = cq.refset(
                    repo = "https://go.googlesource.com/%s" % project,
                    refs = ["refs/heads/%s" % go_branch],
                ),
                allow_submit_with_open_deps = True,
                verifiers = [
                    luci.cq_tryjob_verifier(
                        builder = "try/tricium",
                        location_filters = [
                            cq.location_filter(
                                gerrit_host_regexp = "go-review.googlesource.com",
                                gerrit_project_regexp = filter_project,
                                path_regexp = ".+",
                            )
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
            luci.gitiles_poller(
                name = "%s-%s-trigger" % (project, go_branch_short),
                bucket = "ci",
                repo = "https://go.googlesource.com/%s" % project,
                refs = ["refs/heads/" + go_branch],
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
                    refs = ["refs/heads/" + go_branch],
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
                    refs = ["refs/heads/" + go_branch],
                    entries = make_console_view_entries(postsubmit_builders),
                )

_define_go_ci()

exec("./recipes.star")
