+++
date = "2017-12-17T21:42:41+11:00"
title = "Reduce build time with Docker"
+++

> One weird trick to reduce build time! CI services hate him!

This is a fairly simple way to reduce your build time if you spend a lot of time bundling dependencies prior to performing tests or deployments in CI.

The trick is to bundle any _build_ dependencies into a Docker image so that you don't have to install them each time you run a job.

Let's take a look at my `.gitlab-ci.yml` file that I used in ["GitLab Review Apps with Zeit's Now.sh service"]({{< relref "now-review-app-post.md" >}}):

{{< gist hugomd 8831247d8b8549c981feeaa29b1893b4 >}}

As you can see above, I'm installing build dependencies for `now` with `npm install -g now --silent` in both the `start_review` and `stop_review` pipeline stages. This is unnecessary and is done _every single time_ a job runs (not [DRY](https://en.wikipedia.org/wiki/Don%27t_repeat_yourself)). If you run enough builds, that's a significant amount of time wasted.

{{< tweet 942331411503177728>}}

Let's build a simple Docker image that includes the `now` dependency:

{{< gist hugomd 5642a71d1e94b3a40874a18aa6a69b70>}} 

With a simple `.gitlab-ci.yml` file to push the image to the GitLab container registry:

{{< gist hugomd 5a2b563c564b73b3f04fc88cb5486260 >}}

Now, instead of pulling from `image: node` in our initial `.gitlab-ci.yml`, we can use `image: my.registry/hugo/now-image:latest` and remove the section of our `.gitlab-ci.yml` where we install our build dependencies, because they're already bundled!

{{< gist hugomd 1b1171b964c723bc1b8496a934135b12 >}}

Now we've shaved a bit of time off our builds 👌 

If you liked this post, have questions or queries, or can help me make it better, please [tweet me!](https://twitter.com/hugojmd)
