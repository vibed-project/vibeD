# vibeD
vibeD is a tool that functions as a workload orchestrator for GenAI generated artifacts like websites, web applications and such. At the moment tools like claude, gemini or chatGPT are creating those artifacts and presenting them within their desktop and web apps. Now, for enterprises this isn't ideal as they might expose confidential data. Also, there are open source vibe coding approaches that also need a deployment targtet. 

Towards the vibecoding tools, vibeD is exposed via an MCP server. 

vibeD doesn't run the workload/artifacts by itself. It should support 2 options:
1. Kubernetes native workload as a Pod or Deployment
2. via Knative as a serverless item (preferred)

Therefore, vibeD should check which options are available and package the workload for the right environment. To create the containers and deplyoment artifacts use buildpacks.

vibeD either stores all containerimages and deployment manifests in a local file system (if not different configured or for testing) or can be configured to store the code of the artifacts and the deployment manifest files like help charts in a single github repository where each artifact gets one folder. In addition it should be possible to configure access to a container registry to push container images and pull container images. And a later phase it should be possible to also configure to have per end user who creates artifacts one github or gitlab repository. How to do all this I don't know, please find the best solution.

In addition vibeD should have simple frontend thats shows the deplyoed artifacts and provides a link to website, web app or whatever it is. 

## Documentation
Please create also a nice website for documentation and always update the docs according to your changes.

## Tech Stack
Please write whatever is needed in Go. For the frontend I'm open, but it should be fast and common.

## Development Environment
The local development environment where we can test vibeD is Podman with kind. Please everything that needs to be tested and running, needs to run in the kind cluster.

## Product Environments
We assume vibeD will always run on a Kubernetes cluster, coming with the predefined dependencies.

## Dependencies
vibeD will have dependencies or better to say prerequisits that need to be available and that vibeD communicates to. Out of my head I see for this primary:
* knative as serverless engine

If further pre-installed tools are needed those also will go into the dependencies folder.
Please create deployment templates with Helm for all dependencies.