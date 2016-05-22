+++
date = "2015-10-27T15:35:21+11:00"
draft = false
title = "Tutorial: Roll your own screenshot service with S3 and Dropshare/ShareX"
slug = "roll-your-own-screenshot-service-with-s3"
aliases = [
	"roll-your-own-screenshot-service-with-s3"
]
+++
### What we're going to be doing
1. Setting up a subdomain
2. Setting up Amazon S3
3. Setting up:
	1. Preliminary Setup
	2. ShareX (Windows)
	3. DropShare (Mac)

### Setting up Amazon S3  
**If you already have an S3 Bucket created/setup, skip this section.** 

1. Login to to your [AWS Management Console](https://aws.amazon.com/console/).
2. Go to Amazon S3.  
	[![](https://i.hugo.sx/ss/044wk.png)](http://i.hugo.sx/ss/044wk.png)
3. Create a new Bucket and take a note of the bucket name.
4. Open bucket properties (in the top right) and add a [bucket policy](http://docs.aws.amazon.com/AmazonS3/latest/dev/example-bucket-policies.html):  

		{
		  "Version":"2012-10-17",
		  "Statement":[
			{
			  "Sid":"AddPerm",
			  "Effect":"Allow",
			  "Principal": "*",
			  "Action":["s3:GetObject"],
			  "Resource":["arn:aws:s3:::YOUR-BUCKET-NAME-HERE/*"]
			}
		  ]
		}	

5. This bucket policy will make all of your uploads public. Now you can move on to setting up your subdomain.

### Setting up a subdomain
1. Login to your domain registrar, I use [Gandi](http://gandi.net/).
2. Navigate to your equivalent "My Account" page.
3. On Gandi, you have to click on your domain, and on the following page click "Edit the Zone".
4. Create a new zone.
5. Create a CNAME record:
	6. Set the TTL to default.
	7. Set the name to the subdomain you want, for instance `i`, in my case, for `i.hugo.sx`
	8. Set the value to `your-bucket-name.your-region.amazonaws.com.`, for instance, `my-bucket.s3-ap-southeast-2.amazonaws.com.`
	[![](https://i.hugo.sx/ss/UzDR6.png)](http://i.hugo.sx/ss/UzDR6.png)
6.  Hit save. You'll have to wait somewhere in the range of 24 hours for the DNS populate.


### Uploading to Amazon S3

#### Preliminary setup
1. Go to your [AWS Console](https://console.aws.amazon.com/), click on your name in the top right and then _Security Credentials_.
2. You should be prompted to use _IAM users_, click on _IAM users_.
3. Create a new user and give it a relevant name, e.g. dropshare-mac or sharex-windows, and hit enter.
4. Copy the _Access Key ID_ and _Secret Access Key_ somewhere, you'll need them for the next step.

#### Dropshare (Mac)
[Dropshare](https://getdropsha.re/) costs 20.00 USD, which is quite reasonable for what it does. However, if you're looking for something slightly cheaper and with arguably more features, check out [Dropzone 3](https://aptonic.com/).  

1. Once you've downloaded Dropshare, open up the preferences and create a new Amazon S3 connection:  
2. Fill out the required fields, most a pretty self explanatory. 
3. When filling out the custom domain, don't prefix it with `http://` and don't add `/` as a suffix.
4. If you want to upload to a subfolder, you'll want to add that in with the bucket name.
5. Enjoy!
[![](https://i.hugo.sx/ss/aFAD7.gif)](http://i.hugo.sx/ss/aFAD7.gif)


#### ShareX (Windows)
1. [Download ShareX](http://getsharex.com/).
2. Open up _Destinations_ from the list on the side.
3. Click on _Amazon S3_ under _Image Upload_.
4. Enter the_Access Key_ and _Secret Key_, you should end up with something like this:  [![](https://i.hugo.sx/ss/ZwutV.png)](http://i.hugo.sx/ss/ZwutV.png)
5. Now set Amazon S3 as your upload destination:  [![](https://i.hugo.sx/ss/ZmG4G.jpg)](http://i.hugo.sx/ss/ZmG4G.jpg)

Questions? Comments? [Tweet me](http://twitter.com/hugojmd), or [email me](http://hugo.sx).
