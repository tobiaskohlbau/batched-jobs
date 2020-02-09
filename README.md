# batched-jobs controller

This repository contains a controller which spawns jobs based on messages on a redis channel.
Depending on content a job is spawned which works on the coressponding queue. This scenario
assumes the worker has a significant higher initalisation time than execution time. In this
scenario it's not sufficient to spawn a worker for each queue item. Instead a worker processes
items until the queue is empty for some time. This addresses the issue to have multiple workers
for queues where no items are expected for a longer period of time.

# Instructions

```
export REGISTRY=YOUR_DOCKER_REIGSTRY_HERE
make deploy
make run
```

If you execute `kubectl get jobs` you should observe a job for every topic [1,2,3].
If you stop the execution of the generator the jobs should vanish after 30 seconds.

# Reference

Based on tgik-controller by jbeda shown in TGIK 007, 008 and 009.
https://github.com/jbeda/tgik-controller
