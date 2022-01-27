# Rebasing `zmap/dns` from `miekg/dns`

## Note

This guide assumes that the user is using SSH-based git cloning and therefore assuming the miekg repo is hosted at: `git@github.com:miekg/dns.git`

If that is not the case, then use the HTTPS clone url here in place of the above: `https://github.com/miekg/dns.git`

Also, this guide assumes that the main remote repo of `zmap/dns` is called `origin` - again, subsititute if that is not the case.

## Steps

1. Add `miekg/dns` as a new remote in the repo: `git remote add miekg git@github.com:miekg/dns.git`
2. Checkout the master branch: `git checkout master`
3. Ensure that you have pulled the latest changes from `zmap/dns`: `git pull origin master`
3. Pull and merge in the latest changes from `miekg/dns`: `git pull miekg master`
    - At this point, there _shouldn't_ be any conflicts. Master is not the branch where the ZDNS patching is applied, so it should be a clean merge.
4. Push the changes to `zmap/dns` remote: `git push origin  master`
5. Pull latest tags from `miekg/dns`: `git fetch miekg --all --tags`
6. Run the following command to push the latest tag from `miekg/dns` to `zmap/dns`: `LATEST_TAG=$(git tag --merged | sort -V | tail -n 1); git push origin "${LATEST_TAG}"`
7. Now, switch to the `zdns` branch: `git checkout zdns`
8. Ensure that you have pulled the latest changes from `zmap/dns`: `git pull origin zdns`
8. Create a temporary branch for merging in the latest changes: `git checkout -b temp/rebase`
    - Creating a temp branch increases visibility of the changes going back into `zdns` branch, especially when working directly in the `zmap/dns` repo and not in a fork.
9. Merge the new changes from `miekg/dns` into the `temp/rebase` branch: `git merge master`
    - This will likely result in conflicts.
    - The reason for choosing a merge vs. a rebase is twofold. 
        - First, it provides a more intuitive (though possibly more verbose) commit history, which may be useful as new devs or other inspect project history.
        - Second, and more importantly, it removes the need for the developer doing this process to fix conflicts at each of the 5 or 6 "patch" commits on the `zdns` branch. Because these changes occur in place that often modified by the `miekg` team, each commit ends up with a variety of merge conflicts and it becomes easy to incorrectly fix these conflicts. Merging instead ensures that only one set of conflicts need to be address, if any: the final version of the `miekg/dns` library with the patch that was made.
10. Fix any conflicts that came about in the merge, then add and commit the output `git add --all; git commit -m "address conflicts between miekg and zmap dns libs"`
11. Run tests to ensure that nothing has been broken: `go test github.com/zmap/dns`
12. If all is successful, then push the changes to the remote: `git push origin temp/rebase`
13. Open a PR for the recent changes in the `temp/rebase` branch into `zdns`.
    - This step may be broken down further into two sub-steps if you are working in a fork.
    - First would be merging the `temp/rebase` into `zdns` all within the fork. A PR is recommended (if you've enabled GitHub Actions) to check all functionality again.
    - Second, once this PR has been merged, open a PR from `zdns` branch in `yourfork/dns` to `zdns` branch in `zmap/dns` so that the changes will be incorporated back into this repo.
14. Delete branch `temp/rebase`, which can be done either via the CLI or via the PR in GitHub. 


## Complete guide

For clarity, the steps are provided all together here in more consise form.

```bash
git remote add miekg git@github.com:miekg/dns.git
git checkout master
git pull origin master
git pull miekg master
git push origin master
git fetch miekg --all --tags
LATEST_TAG=$(git tag --merged | sort -V | tail -n 1); git push origin "${LATEST_TAG}"
git checkout zdns
git pull origin zdns
git checkout -b temp/rebase
git merge master
# FIX CONFLICTS
git add --all; git commit -m "address conflicts between miekg and zmap dns libs"
go test github.com/zmap/dns
git push origin temp/rebase
# OPEN PR (as described above)
# DELETE TEMP BRANCH
```