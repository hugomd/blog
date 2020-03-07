+++
date = "2018-12-03T00:00:00+11:00"
title = "Building lean Docker images"
tags = ["docker"]
+++

This was originally posted on [Medium](https://medium.com/localz-engineering/building-lean-docker-images-47b5a896688e).

At Localz, we use Docker as the building block for all of our deployments. We like to think of it as a single unit of deployment. If it runs in Docker, it can be deployed and horizontally scaled, and requires less maintenance than running on bare metal.

In this post, I’ll go through the techniques we use to keep our Docker images lean — including using base images, removing build dependencies, utilising build stages, and scratch-based images!

### Separating build and runtime dependencies

No matter which language you use — whether it’s Node.js, Go, Python, or something else — it’s almost inevitable that you’ll have both build and runtime dependencies.

In brief, build dependencies are modules, libraries, or packages that are required to setup your application — which may mean compiling, testing or otherwise ‘building’ your code.

Runtime dependencies are specifically required while your application is **_running_**. Examples of these are pretty easy to identify — database ORMs, third party SDKs, and any module your project depends on to perform required tasks.

Separating these dependencies can shave down the size of a Docker image both in number of intermediary layers, and size on disk. If you’re deploying a lot of images, reducing image size here and there can result in pretty big savings.

If you’re using Alpine-based images and require build tooling like `make` and `g++`, you can install those dependencies, use them, and remove them afterwards:

{{< gist hugomd 423fc3a2f0dcc7b93323790701bc09d6 >}}

With build dependencies, the image weighs in at 218 MB. But when we remove them, it is only 67.1 MB — that’s 150.9 MB we’re doing without!

### Consolidating shared dependencies into a base image

If you have more than a few applications sharing the same stack and some of the same dependencies, you can create a base image to reduce total build time. This means that rather than re-installing shared dependencies for every build you can pull the layers in binary form, no compilation/installation required!

Let’s take a look at a simple example. Suppose we use [Now](https://zeit.co/now) for deployment, meaning we call `now` to deploy in our continuous deployment pipeline. Instead of calling `npm install -g now` for every single build, we can cache this in a _parent_ image and enjoy faster builds.

We define our parent image with:

{{< gist hugomd a9b88fbd9bb45d4d0f305f73137d48fc >}}

We can then build this image and tag it as `parent`

```bash
docker build --file parent.Dockerfile --tag parent .
```

Now we can make a _child_ image which is based upon the _parent_:

{{< gist hugomd 5e2d05f9a336e7aec2a89dd5c3b73012 >}}

While this example may seem trivial as `now` doesn't take long to install, if you're using an npm package which uses C/C++ bindings then you might have to deal with compilation time, which can be fairly significant when building in a CI tool that limits CPU cores.

You can read more about base images in the [Docker documentation](https://docs.docker.com/develop/develop-images/baseimages/).

### Build stages and scratch-based images

This example pertains more to compiled applications. In these cases you can compile your application using one Docker image, and then copy the binary into a [`scratch`](https://docs.docker.com/samples/library/scratch/) image as a single binary. `scratch` images have no base layers, so they’re super lightweight!

{{< gist hugomd de3893012564cd7d45b660decb4a9016 >}}

If we were to use `golang:1.11.2-alpine3.8` as the base image, rather than `scratch`, our image would be a whopping 312 MB! With `scratch`, it’s just 6.32 MB! 😱

That’s it! I hope this proves useful, and if you weren’t already keen on Docker, maybe you are now!

If you’ve got any questions, feel free to hit me up on [Twitter](https://twitter.com/hugojmd) or [Mastodon](https://melb.social/@hugo), I’m always keen to help out! ✌️
