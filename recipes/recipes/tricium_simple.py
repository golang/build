# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

from recipe_engine import post_process

DEPS = [
  'depot_tools/gerrit',
  'depot_tools/git',
  'depot_tools/tryserver',
  'recipe_engine/buildbucket',
  'recipe_engine/path',
  'recipe_engine/platform',
  'recipe_engine/tricium',
]

def RunSteps(api):
  """This recipe runs quick analyzers for Go repos.
  """
  project = api.tryserver.gerrit_change_repo_project
  commit_message = api.gerrit.get_change_description(
      'https://%s' % api.tryserver.gerrit_change.host,
       api.tryserver.gerrit_change.change, api.tryserver.gerrit_change.patchset)
  repo_path = api.path['start_dir'].join(project)
  url = 'https://%s/%s' % (api.tryserver.gerrit_change.host, api.tryserver.gerrit_change.project)
  ref = "refs/changes/%d/%d/%d" % (api.tryserver.gerrit_change.change%100,
                                   api.tryserver.gerrit_change.change,
                                   api.tryserver.gerrit_change.patchset)
  api.git.checkout(url=url, ref=ref, dir_path=repo_path, submodules=False)
  affected_files = api.tryserver.get_files_affected_by_patch(
      patch_root=project,
      report_files_via_property='affected_files')
  analyzers = [
        api.tricium.analyzers.HTTPS_CHECK,
        api.tricium.analyzers.SPELLCHECKER,
        api.tricium.analyzers.INCLUSIVE_LANGUAGE_CHECK,
        api.tricium.analyzers.COPYRIGHT,
  ]
  api.tricium.run_legacy(analyzers, repo_path, affected_files, commit_message)

def GenTests(api):

  def test_with_patch(name, affected_files):
    test = api.test(
        name,
        api.buildbucket.try_build(
            project='go',
            builder='tricium-simple',
            git_repo='https://go.googlesource.com/go',
            change_number=86753,
            patch_set=1),
        api.platform('linux', 64),
    )
    existing_files = [
        api.path['start_dir'].join('go', f) for f in affected_files
    ]
    test += api.path.exists(*existing_files)
    return test

  yield test_with_patch('one_file', ['README.md']) + api.post_check(
      post_process.StatusSuccess) + api.post_process(
          post_process.DropExpectation)

  yield test_with_patch('many_files', [
      'go.mod',
      'go.sum',
      'build.go',
      'LICENSE',
      'README.md',
      'main.go',
  ]) + api.post_check(post_process.StatusSuccess) + api.post_process(
      post_process.DropExpectation)
