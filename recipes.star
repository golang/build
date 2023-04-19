"""Recipe bundler builder definition.

It fetches recipes from the repository and bundles them and all their transitive
dependencies as a CIPD package, which is then used by builders that use
`luci.recipe(...)` as an executable.
"""

def bundler(
        name,
        repo_specs,
        triggered_by = None):
    luci.builder(
        name = name,
        bucket = "prod",
        priority = 20,
        description_html = "Builder to bundle recipe changes and upload bundled recipe package to CIPD.",
        executable = luci.recipe(
            name = "recipe_bundler",
            cipd_package = "infra/recipe_bundles/chromium.googlesource.com/infra/infra",
            cipd_version = "git_revision:910dab144e53063038e4e67a7d7bac729203f43c",
            use_bbagent = True,
            use_python3 = True,
        ),
        dimensions = {
            "pool": "luci.golang.prod",
            "os": "Ubuntu",
            "cpu": "x86-64",
        },
        properties = {
            # This property controls the version of the recipe_bundler go tool:
            #   https://chromium.googlesource.com/infra/infra/+/master/go/src/infra/tools/recipe_bundler
            "recipe_bundler_vers": "git_revision:910dab144e53063038e4e67a7d7bac729203f43c",
            # These control the prefix of the CIPD package names that the tool
            # will create.
            "package_name_prefix": "golang/recipe_bundles",
            # The CIPD package prefix for internal recipes.
            "package_name_internal_prefix": "golang/recipe_bundles",
            # Where to grab the recipes to bundle.
            "repo_specs": repo_specs,
        },
        service_account = "recipe-bundler@golang-ci-luci.iam.gserviceaccount.com",
        expiration_timeout = 15 * time.minute,
        execution_timeout = 10 * time.minute,
        triggered_by = triggered_by,
    )
    luci.list_view_entry(
        builder = name,
        list_view = "prod-builders",
    )

bundler(
    name = "recipe-bundler",
    repo_specs = [
        "go.googlesource.com/build=FETCH_HEAD,refs/heads/luci-config",
    ],
    triggered_by = [
        luci.gitiles_poller(
            name = "build-luci-config-poller",
            bucket = "prod",
            repo = "https://go.googlesource.com/build",
            refs = ["refs/heads/luci-config"],
        ),
    ],
)
