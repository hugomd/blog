+++
date = "2017-12-17T21:42:41+11:00"
title = "Reduce build time with Docker"
description = "🐳"
+++

> One weird trick to reduce build time! CI services hate him!

This is a fairly simple way to reduce your build time if you spend a lot of time bundling dependencies prior to performing tests or deployments in CI.

The trick is to bundle any _build_ dendencies into a Docker image so that you don't have to install them each time you run CI.

Let's take a look at my `.gitlab-ci.yml` file that I used in ["GitLab Review Apps with Zeit's Now.sh service"]({{< relref "now-review-app-post.md" >}}):

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

As you can see above, I'm installing build dependencies for `now` with `npm install -g now --silent` in both the `start_review` and `stop_review` pipeline stages. This is unnecessary and is done _every single time_ a job runs. If you run enough builds, that's a significant amount of time wasted.

{{< tweet 942331411503177728>}}

Let's build a nice Docker image that includes the `now` dependency:

```
FROM node:alpine

RUN npm install -g --silent --unsafe-perm now
```

With a simple `.gitlab-ci.yml` file to push the image to the GitLab container registry:

```
image: docker:latest

variables:
  DOCKER_DRIVER: overlay

services:
  - docker:dind

stages:
  - push

push:
  stage: push
  script:
    - docker login -u gitlab-ci-token -p $CI_BUILD_TOKEN my.registry # This could just be Dockerhub
    - docker build -t my.registry/hugo/now-image .
    - docker push my.registry/hugo/now-image:latest # Ideally should be versioning, but this is just a tutorial
  only:
    - master
```

Now, instead of pulling from `image: node` in our initial `.gitlab-ci.yml`, we can use `image: my.registry/hugo/now-image:latest` and remove the installation section of our `.gitlab-ci.yml` file:

```
image: my.registry/hugo/now-image:latest

stages:
  - review

variables:
  REPO_NAME: now-review-app

start_review:
  stage: review
  script:
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

Now we've shaved a bit of time off our builds 👌 
