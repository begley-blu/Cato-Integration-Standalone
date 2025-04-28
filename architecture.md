
## Updates for architecture.md

# Deity - Dave's Exchange Interface and Transformation Yeoman
#### A Modular Data Processing Pipeline

Deity is a flexible, modular pipeline for processing data from various sources. It's designed as a pluggable system with interchangeable components for each stage of the data processing workflow.

## Core Concepts

The pipeline consists of four main component types:

1. **Input Modules** - Read data from sources (files, APIs, etc.)
2. **Parser Modules** - Transform raw data into structured formats
3. **Output Modules** - Send processed data to destinations
4. **Auth Providers** - Handle authentication with external services

## Key Features

### Multi-Auth Support
- Configure multiple authentication providers simultaneously
- Different inputs/outputs can use different auth providers
- Named authentication profiles for easy reference

### Configuration Routing
- Define explicit routes between inputs and outputs
- Each route can have its own parser configuration
- Default routing when no explicit routes are defined

### Data Filtering
- Filter data within routes based on field values
- Support for simple field comparison and complex expressions
- Efficient processing by filtering early in the pipeline

## Architecture

For a visual representation of Deity's architecture and component interactions, see the [Architecture Diagram](architecture.md). This flowchart illustrates the relationships between core components, module types, and the data processing flow.

```mermaid
flowchart TB
    %% Setting flow direction and layout options 
    %% Node definitions with IDs
    subgraph Core["Core Components"]
        direction LR
        main["main.go - Pipeline Controller"]
        registry["internal/registry/registry.go - Module Registry"]
        config["internal/config/config.go - Configuration Management"]
        cli["internal/cli/cli.go - Command Line Interface"]
        logger["internal/logger/logger.go - Logging System"]
        filter["internal/filter/filter.go - Data Filtering Engine"]
    end

    subgraph Module_Types["Module Types"]
        authProvider["Registry Interfaces - AuthProvider"]
        inputModule["Registry Interfaces - InputModule"]
        parserModule["Registry Interfaces - ParserModule"]
        outputModule["Registry Interfaces - OutputModule"]
    end

    subgraph Inputs["Input Modules"]
        in_file["internal/input/in_file.go - File Input"]
        in_cato["internal/input/in_cato.go - Cato Networks API"]
        in_s3["internal/input/in_s3.go - Amazon S3 Input"]
        in_cisco_sc["internal/input/in_cisco_secure_client.go - Cisco Secure Client"]
        in_cisco_se["internal/input/in_cisco_secure_endpoint.go - Cisco Secure Endpoint"]
    end

    subgraph Parsers["Parser Modules"]
        parse_cef["internal/parser/parse_cef.go - CEF Parser"]
    end

    subgraph Outputs["Output Modules"]
        out_stdout["internal/output/out_stdout.go - Stdout Output"]
        out_file["internal/output/out_file.go - File Output"]
        out_syslog["internal/output/out_syslog.go - Syslog Output"]
    end

    subgraph Auth["Auth Providers"]
        auth_env["internal/auth/auth_env.go - Environment Auth"]
        auth_aws["internal/auth/auth_aws.go - AWS Auth"]
        auth_gcp["internal/auth/auth_gcp.go - GCP Auth"]
    end

    subgraph Config_Files["Configuration Files"]
        default_config["conf/default.yaml - Default Configuration"]
        cato_config["conf/cato.yaml - Cato Networks Configuration"]
        meraki_config["conf/meraki.yaml - Meraki Configuration"]
        cisco_sc_config["conf/cisco_sc.yaml - Cisco Secure Client Config"]
        cisco_se_config["conf/cisco_secure_endpoint.yaml - Cisco Secure Endpoint Config"]
        s3_config["conf/s3.yaml - Amazon S3 Configuration"]
    end

    subgraph Routes["Pipeline Routes"]
        route1["Route 1: Input A → Parser → Output X"]
        route2["Route 2: Input B → Parser → Output Y, Output Z"]
        route3["Route 3: Input C → Custom Parser → Output X"]
        route_filters["Route Filters: Field Matching & Expressions"]
    end

    %% Core connections with specific layout control
    main --> registry
    main --> config
    main --> cli
    main --> logger
    main --> filter
    
    %% Module Type connections with controlled routing
    registry --- Module_Types
    authProvider --- Auth
    inputModule --- Inputs
    parserModule --- Parsers
    outputModule --- Outputs

    %% Main pipeline flow with labeled connections and path routing
    main -->|"1-Load Config"| config
    config -->|"2-Config Files"| Config_Files
    main -->|"3-Init Modules"| registry
    registry -->|"4-Create Auth"| Auth
    registry -->|"5-Create Inputs"| Inputs
    registry -->|"6-Create Parser"| Parsers
    registry -->|"7-Create Outputs"| Outputs
    main -->|"8-Setup Routes"| Routes
    main -->|"9-Process Data"| Inputs
    Inputs -->|"10-Raw Data"| Routes
    Routes -->|"11-Apply Filters"| filter
    Routes -->|"12-Parse Data"| Parsers
    Parsers -->|"13-Structured Data"| Outputs

    %% Registry registration with improved routing
    Auth --->|"Register"| registry
    Inputs --->|"Register"| registry
    Parsers --->|"Register"| registry
    Outputs --->|"Register"| registry

    %% CLI commands with specific routing to avoid crossings
    cli -->|"run"| main
    cli -->|"init"| config
    cli -->|"validate"| config
    cli -->|"list-modules"| registry
    cli -->|"module-info"| registry

    %% Apply styles to node groups
    class Core core
    
    %% Make all connections orthogonal with right angles
    linkStyle default orthogonal
