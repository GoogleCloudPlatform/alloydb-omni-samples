# Experimental Tools

This space is dedicated to hosting community-driven tools and projects that integrate with or enhance AlloyDB Omni. The code found here is designed to be functional but may be in an experimental state.

### Disclaimer

**The tools and projects in this directory are not officially supported by Google.** They are provided as-is for the benefit of the community. Use them at your own discretion.

---

### Projects

This directory contains the following projects, each in its own sub-folder:

*   **[`pgrr/`](./pgrr/)**: A project for recording production database traffic using `pgrr` and replaying it against an AlloyDB Omni cluster. This is invaluable for performance testing, benchmarking, and migration validation.

*   **[`observability-stack/`](./observability-stack/)**: Deploys a complete, self-contained observability stack to help users monitor the health and performance of their AlloyDB Omni fleet. This project includes open-source tooling (e.g., Prometheus, Grafana) pre-configured with dashboards and alerts.

### Contribution Guidelines for this Directory

If you are contributing a new tool or making changes to an existing one within this `experimental-tools` directory, please adhere to the following guidelines:

1.  **Self-Contained Projects**: Each tool must be entirely contained within its own sub-folder (e.g., `observability-stack/`). All source code, configuration, scripts, and documentation for the project must reside within that folder.

2.  **Mandatory `README.md`**: Each project sub-folder **must** contain its own `README.md` file with detailed, step-by-step instructions covering:
    *   A clear description of the tool and its purpose.
    *   All prerequisites required to build and run the tool.
    *   Installation, deployment, and usage instructions.
    *   Cleanup steps to remove all created resources or artifacts.

3.  **General Contribution Process**: For the mechanics of contributing (forking the repository, creating a pull request, signing the CLA), please follow the main repository guidelines documented in the top-level [**CONTRIBUTING.md**](https://github.com/GoogleCloudPlatform/alloydb-omni-samples/blob/main/CONTRIBUTING.md).
