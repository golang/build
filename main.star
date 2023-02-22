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

        # Allow any googler to see bulders.
        luci.binding(
            roles = "role/buildbucket.reader",
            groups = "googlers",
        ),
    ],
    acls = [
        acl.entry(
            roles = acl.PROJECT_CONFIGS_READER,
            groups = "googlers",
        ),
        acl.entry(
            roles = acl.CQ_COMMITTER,
            groups = "authenticated-users",
        ),
        acl.entry(
            roles = acl.CQ_DRY_RUNNER,
            groups = "authenticated-users",
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

MAIN_REPO_PORTS = {
    "linux-amd64": {"os": "Linux", "cpu": "x86-64"},
    "windows-amd64": {"os": "Windows", "cpu": "x86-64"},
}
MAIN_REPO_BRANCHES = {
    "master":"master",
    "go1.20":"release-branch.go1.20",
}

# To begin with, use a smaller set of ports and branches
# for testing golang.org/x repos.
OTHER_REPO_PORTS = {
    "linux-amd64": {"os": "Linux", "cpu": "x86-64"},
}
OTHER_REPO_BRANCHES = {
    "master":"master",
}

def _define_go_ci():
    # Main Go repo.
    for branch_name, ref in MAIN_REPO_BRANCHES.items():
        luci.cq_group(
            # cq group names must match "^[a-zA-Z][a-zA-Z0-9_-]{0,39}$"
            name = ("go_repo_%s" % branch_name).replace(".", "-"),
            watch = cq.refset(
                repo = "https://go.googlesource.com/go",
                refs = ["refs/heads/%s" % ref]
            ),
            allow_submit_with_open_deps = True,
        )
        builders = []
        for port, dimensions in MAIN_REPO_PORTS.items():
            name = "%s-%s" %(port, branch_name)
            for bucket in ["ci", "try"]:
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
                    properties = {
                        "project": "go",
                    },
                    service_account = "luci-task@golang-ci-luci.iam.gserviceaccount.com",
                )
            builders.append("ci/%s" % name)
            luci.cq_tryjob_verifier(
                builder = "try/%s" % name,
                # cq group names must match "^[a-zA-Z][a-zA-Z0-9_-]{0,39}$"
                cq_group = ("go_repo_%s" % branch_name).replace(".", "-"),
            )
        luci.gitiles_poller(
            name = "go-%s-trigger" % branch_name,
            bucket = "ci",
            repo = "https://go.googlesource.com/go",
            refs = ["refs/heads/" + ref],
            triggers = builders,
        )
        luci.console_view(
            name = "go-%s-ci" % branch_name,
            repo = "https://go.googlesource.com/go",
            title = "go %s" % branch_name,
            refs = ["refs/heads/" + ref],
            entries = [
                luci.console_view_entry(builder = builder, category = builder.split('-')[0])
                for builder in builders
            ],
        )

    # golang.org/x repos.
    for project in ["build", "image"]:
        for branch_name, ref in OTHER_REPO_BRANCHES.items():
            luci.cq_group(
                name = "%s_repo_%s" % (project, branch_name),
                watch = cq.refset(
                    repo = "https://go.googlesource.com/%s" % project,
                    refs = ["refs/heads/%s" % ref]
                ),
                allow_submit_with_open_deps = True,
            )
            builders = []
            for port, dimensions in OTHER_REPO_PORTS.items():
                name = "%s-%s-%s" %(project, port, branch_name)
                for bucket in ["ci", "try"]:
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
                        properties = {
                            "project": project,
                        },
                        service_account = "luci-task@golang-ci-luci.iam.gserviceaccount.com",
                    )
                builders.append("ci/%s" % name)
                luci.cq_tryjob_verifier(
                    builder = "try/%s" % name,
                    cq_group = "%s_repo_%s" % (project, branch_name),
                )
            luci.gitiles_poller(
                name = "%s-%s-trigger" % (project, branch_name),
                bucket = "ci",
                repo = "https://go.googlesource.com/%s" % project,
                refs = ["refs/heads/" + ref],
                triggers = builders,
            )
            luci.console_view(
                name = "%s-%s-ci" % (project, branch_name),
                repo = "https://go.googlesource.com/%s" % project,
                title = "x/%s %s" % (project, branch_name),
                refs = ["refs/heads/" + ref],
                entries = [
                    luci.console_view_entry(builder = builder, category = builder.split('-')[0])
                    for builder in builders
                ],
            )

_define_go_ci()

exec("./recipes.star")
