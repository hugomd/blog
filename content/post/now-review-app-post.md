+++
date = "2016-12-29T17:53:11+01:00"
title = "GitLab Review Apps with Zeit's Now.sh service"
description = "GitLab & Now are 👌"
tags = ["gitlab", "zeit", "now"]
+++

[Zeit](https://zeit.co/about) is a great little company that aims to “Make Cloud Computing as Easy and Accessible as Mobile computing”, and they’re doing that just with their relatively new service, _[Now](https://zeit.co/now)_. _Now_ aims to make deploying Node and Docker apps as simple as typing `now` into your terminal. It also allows deploying static websites, which is what I’ll be using it for in this little tutorial.

## Prerequisites 
This tutorial assumes that you’ve got a Zeit account, and have some familiarity with [GitLab Review Apps](https://about.gitlab.com/2016/11/22/introducing-review-apps/), [GitLab CI](https://about.gitlab.com/gitlab-ci/), and that you have a [GitLab Runner](https://docs.gitlab.com/runner/) setup and ready to go.

## Setting up your Project
To get started, let’s create a new GitLab project, `now-review-app`. 
![](DraggedImage.png)

Next, clone the repository:
```
git clone URL_TO_YOUR_REPOSITORY_
```

Create the `.gitlab-ci.yml` file:
```
touch .gitlab-ci.yml
```

And create an `src` folder:
```
mkdir src
```

Inside `src`, place an `index.html` file, with the contents:
```
<h1>Example HTML file</h1>
```

Modify `.gitlab-ci.yml` with the contents:
```
image: node

stages:
  - review

variables:
  REPO_NAME: now-review-app

start_review:
  stage: review
  script:
    - npm install -g now --silent
    - URL=$(now --static -t ${NOW_TOKEN} ./src -n ${REPO_NAME}-${CI_BUILD_REF_SLUG})
    - NOW_URL=$(echo ${URL} | sed s/'http:\/\/'/''/g | sed s/'https:\/\/'//g)
    - now -t ${NOW_TOKEN} alias set ${NOW_URL} ${REPO_NAME}-${CI_BUILD_REF_SLUG}.now.sh
  environment:
    name: review/$CI_BUILD_REF_NAME
    url: https://$REPO_NAME-$CI_BUILD_REF_SLUG.now.sh
    on_stop: stop_review
  only:
    - branches
  except:
    - master

stop_review:
  stage: review
  script:
    - npm install -g now --silent
    - now rm -t ${NOW_TOKEN} -y ${CI_BUILD_REF_SLUG}
  when: manual
  environment:
    name: review/$CI_BUILD_REF_NAME
    action: stop
  only:
    - branches
  except:
    - master
```

Commit and push this to GitLab.

## Testing it out
Add a new variable called `NOW_TOKEN` to the project, accessible via the project menu. This variable will contain a _Now_ token. You can create a new token on [this page](https://zeit.co/account#api-tokens).
![](DraggedImage-1.png)

Create a new branch with:
```
git checkout -b feature/modify-app
```

Modify the contents of the `index.html` file we created earlier, we’ll be changing it to:
```
<h1>Modified Example HTML source file.</h1>
```

Commit and push this to GitLab.

Next, create a merge request from `feature/modify-app` into `master`, and wait for the pipeline to finish building, this will create the review app:
![](DraggedImage-2.png)

If everything is successful, your pipeline should pass, and you should now see a link to the review app on the merge request:
![](DraggedImage-3.png)

Clicking on it should take you to the _Now_ deployment with the correct changes to `index.html` displayed:
![](DraggedImage-4.png)

It’s worth noting here this link is an alias for the latest review app deployment. You’ll have to use `now ls` to view all iterations of the merge request, if required. 

If you found this interesting, have any questions or queries, or just want to say hi, then hit me up on [Twitter](https://twitter.com/hugojmd)!
