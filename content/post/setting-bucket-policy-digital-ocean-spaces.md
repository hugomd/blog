---
title: "Setting Bucket Policy DigitalOcean Spaces"
date: 2021-08-24T11:15:32+10:00
sequence: 11
---
{{% 100daystooffload %}}

I've recently set up [PeerTube](https://github.com/Chocobozzz/PeerTube/) for personal use, after native support for [object storage](https://github.com/Chocobozzz/PeerTube/pull/4290) was merged.

It's running on DigitalOcean Kubernetes, and using DigitalOcean Spaces for object storage.

I quickly ran into issues. Any video uploaded to PeerTube was inaccessible, because the object ACL was set to private. I could manually set them to public read in DigitalOcean's UI, or get PeerTube to set it, but both seemed like bad options. DigitalOcean does support enabling public listing, which means all the objects in your bucket can be listed as long as someone has the bucket URL. This naturally isn't great, because if someone uploads a private video to PeerTube, anyone can list all of the objects in the bucket and end up finding it anyway.

After pulling on the thread long enough, I found that Spaces supports bucket policies, which allow you to specify a global ACL that applies to all objects.

To apply this policy, you need to set up AWS CLI:
```bash
brew install awscli
aws configure
```

The arguments to `aws configure` are the same as those described in DigitalOcean's [guide to set up `s3cmd`](https://docs.digitalocean.com/products/spaces/resources/s3cmd/).

Next, you can save the following to `policy.json`, replacing `BUCKET_NAME` with the name of your bucket:
```json
{
    "Version": "2008-10-17",
    "Statement": [
        {
        "Sid": "AddPerm",
        "Effect": "Allow",
        "Principal": "*",
        "Action": "s3:GetObject",
        "Resource": "arn:aws:s3:::BUCKET_NAME/*"
        }
    ]
}
```

You can then apply the policy with, replacing `BUCKET_NAME` with the name of your bucket, and `REGION` with the bucket region (e.g. `sgp1`):
```bash
aws s3api --endpoint=https://REGION.digitaloceanspaces.com put-bucket-policy --bucket BUCKET_NAME --policy file://policy.json
```

Now any objects uploaded to your bucket will be publicly readable, but no one will be able to list the contents of the bucket in their entirety.
