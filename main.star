#!/usr/bin/env lucicfg

lucicfg.check_version("1.33.7", "Please update depot_tools")

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
        # Allow owners to submit any task in any pool.
        luci.binding(
            roles = [
                "role/swarming.poolOwner",
                "role/swarming.poolUser",
                "role/swarming.taskTriggerer",
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
        )
    ],
    acls = [
        acl.entry(
            roles = acl.PROJECT_CONFIGS_READER,
            groups = "googlers",
        ),
    ],
)

# Per-service tweaks.
luci.logdog(gs_bucket = "logdog-golang-archive")

# Realms with ACLs for corresponding Swarming pools.
luci.realm(name = "pools/ci")
luci.realm(name = "pools/try")

# Global recipe defaults
luci.recipe.defaults.cipd_version.set("refs/heads/main")
luci.recipe.defaults.use_python3.set(True)

# The try bucket will include builders which work on pre-commit or pre-review
# code.
luci.bucket(name = "try")

# The ci bucket will include builders which work on post-commit code.
luci.bucket(name = "ci")

# The prod bucket will include builders which work on post-commit code and
# generate executable artifacts used by other users or machines.
luci.bucket(name = "prod")

# This is the cipd package where the recipe bundler will put the built recipes.
# This line makes it the default value for all `luci.recipe` invocations in
# this configuration.
luci.recipe.defaults.cipd_package.set("infra/recipe_bundles/go-review.googlesource.com/build")

# This sets the default CIPD ref to use in builds to get the right version of
# recipes for the build.
#
# The recipe bundler sets CIPD refs equal in name to the git refs that it
# processed the recipe code from.
#
# Note: This will cause all recipe commits to automatically deploy as soon
# as the recipe bundler compiles them from your refs/heads/luci-config branch.
cipd_version = "refs/heads/luci-config"

luci.builder(
  name = "Example Builder",
  bucket = "ci",

  executable = luci.recipe(
    # The name of the recipe we just added.
    name = "hello_world",
  ),
 service_account = "luci-task@golang-ci-luci.iam.gserviceaccount.com",
  schedule = "with 1m interval",
)
