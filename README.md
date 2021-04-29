Litestream as Library
=====================

This repository is an example of embedding Litestream as a library in a Go
application. The Litestream API is not stable so you may need to update your
code in the future when you upgrade.


## Install

To install, run:

```sh
go install .
```

You should now have a `litestream-library-example` in `$GOPATH/bin`.


## Usage

This example application uses AWS S3 and only provides a `-bucket` configuration
flag. It will pull AWS credentials from environment variables so you will need
to set those:

```sh
export AWS_ACCESS_KEY_ID=xxxxxxxxxxxxxxxxxxxx
export AWS_SECRET_ACCESS_KEY=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

You'll need to setup an S3 bucket and use that name when running the app.

```sh
litestream-library-example -dsn /path/to/db -bucket YOURBUCKETNAME
```

On your first run, it will see that there is no snapshot available so the
application will create a new database. If you restart the application then
it will see the local database and use that.

If you remove the database:

```
rm /path/to/db* /path/to/.db-litestream
```

Then when you restart the application, it will fetch the latest snapshot and
replay all WAL files up to the latest position.


## Synchronous replication

This repository provides an example of confirming that the replica syncs to S3
before returning to the caller. Replicating to S3 can be slow so you may end 
up waiting several hundred milliseconds before the sync returns.

