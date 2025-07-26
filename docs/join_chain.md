# Setting up your chain
## Prerequisites

### Dependencies

You will need Docker installed to handle launching your containers

### Files

You will need two files to start:

1. launch_chain.sh - this is the script that will launch your chain with parameters
2. docker-compose-local.yml - This a docker-compose file for launching your chain locally, called by launch_chain.sh

### config.env

To start, you will need to create a file with the following environment variables:

* KEY_NAME - A name for the key and for the two nodes (API and node) that will be created.
* NODE_CONFIG - This is a path to a file that contains information about your inference nodes (see below)
* ADD_ENDPOINT - This is the public url for an endpoint that is already in the chain, that will be used to add your account to the chain.
* PORT - This is the local port that will be exposed for your API endpoint
* PUBLIC_IP - This is the host you will use to expose your own endpoint. It will need to map ultimately to the API container created during this process.

## NODE_CONFIG

This is a json file that defines the endpoints of the inference servers that will actually serve inference requests for your node. The format is as follows:
```
[  
    {  
        "id": "uniqueNodeName",  
        "url": "http://35.76.234.56:8080/",  
        "max_concurrent": 10,  
        "models": [  
            "Qwen/Qwen2.5-7B-Instruct"  
        ]  
    }  
]
```
`max_concurrent` specifies how many requests can be handled by a given endpoint at a time. The API will balance all your requests between all endpoints you specify in this file


## Launching

Once you have created the config.env file with the right settings, you simply launch `./launch_chain.sh` - This will:

1. Start your node, which will connect to the chain using seed nodes specified in the Docker image, and then add you as a participant in the network. It will take some time to catch up to the current chain height.
2. Start your API, which will connect to your node and will be your main entry point, serving inference requests and allowing you to manage your nodes.

## Customizing
Your environment may differ, as to where you want to deploy or how. But you can use `launch_chain.sh` and `docker-compose-local.yml` as a starting point for your own deployment. 

The important points are:

1. Using the Docker images contained in `docker-compose-local.yml` - these are the images that will be used to join the chain. They include all the necessary genesis state and seed nodes to join.
2. {{Need to add info about sharing keys from the Node to the API, once we've made that remotely secure }}
3. Each docker container will need a "KEY_NODE" set to use as the key and the main URL or the API.
3. Adding inference nodes is done through the /v1/nodes/batch endpoint. You can see the call in `launch_chain.sh`
4. To add your node to the network, you will still need the URL of a node that is already joined. `launch_chain.sh` shows how to get the data for the payload (by executing commands on the node) and how to structure it. 
