# Spegel: Visual Architecture Guide

This document provides a comprehensive set of diagrams explaining Spegel's architecture, flows, and operations.

## 1. High-Level Cluster Architecture

Shows how Spegel pods form a P2P network within the cluster. Containerd interacts with Spegel for image pulls and handles fallback to external registry when needed.

```mermaid
graph TB
    subgraph "External"
        ER["External Registry"]
    end

    subgraph "Kubernetes Cluster"
        subgraph "Node 1"
            SP1["Spegel Pod"]
            CD1["Containerd"]
            SP1 <-->|interacts| CD1
            CD1 -->|fallback| ER
        end
        
        subgraph "Node 2"
            SP2["Spegel Pod"]
            CD2["Containerd"]
            SP2 <-->|interacts| CD2
            CD2 -->|fallback| ER
        end
        
        subgraph "Node 3"
            SP3["Spegel Pod"]
            CD3["Containerd"]
            SP3 <-->|interacts| CD3
            CD3 -->|fallback| ER
        end

        SP1 <-->|P2P Network| SP2
        SP2 <-->|P2P Network| SP3
        SP3 <-->|P2P Network| SP1
    end
```

## 2. Pod Component Architecture

Details the internal components of a Spegel pod and their relationships, showing how the registry service, P2P components, and state management interact with each other and with containerd.

```mermaid
graph TB
    subgraph "Spegel Pod"
        subgraph "Registry Service"
            RS[HTTP Server /v2/]
            RH[Request Handler]
            RS --> RH
        end

        subgraph "P2P Components"
            P2P[P2P Router]
            DHT[DHT Provider]
            BS[Bootstrapper]
            P2P --> DHT
            BS --> P2P
        end

        subgraph "State Management"
            ST[State Tracker]
            MT[Metrics]
            ST --> MT
        end

        CD[Containerd Client]
        
        RH --> P2P
        ST --> P2P
        CD --> ST
    end

    subgraph "Node Components"
        CDD[Containerd Daemon]
        CS[Content Store]
        CDD --> CS
    end

    CD --> CDD
```

## 3. Image Pull Flow

Shows the sequence of operations during an image pull request, demonstrating both successful peer pulls and fallback to external registry.

```mermaid
sequenceDiagram
    participant CD as Containerd
    participant SR as Spegel Registry
    participant P2P as P2P Router
    participant PR as Peer Registry
    participant ER as External Registry

    Note over SR,P2P: 20ms default resolve timeout
    Note over SR,P2P: 3 default resolve retries

    CD->>SR: GET /v2/{name}/manifests/{ref}
    SR->>P2P: Resolve(key, allowSelf, retries)
    
    alt Peer Found
        P2P-->>SR: Return Peer Address
        SR->>PR: Request Content
        PR-->>SR: Stream Content
        SR-->>CD: Return Content
        CD->>CS: Store Content
    else No Peers Available (within 20ms)
        SR-->>CD: 404 Not Found
        CD->>ER: Request from External
        ER-->>CD: Return Content
        CD->>CS: Store Content
    end
```

## 4. P2P Network Formation

Shows how nodes discover each other and form the P2P network through leader election and peer sharing.

```mermaid
sequenceDiagram
    participant N1 as Node 1
    participant N2 as Node 2
    participant N3 as Node 3
    participant LE as Leader Election
    
    Note over N1,LE: 10s lease duration
    Note over N1,LE: 5s renew deadline
    Note over N1,LE: 2s retry period

    N1->>LE: Participate in Election
    N2->>LE: Participate in Election
    N3->>LE: Participate in Election
    LE->>N1: Elected Leader
    N2->>N1: Discover Leader
    N3->>N1: Discover Leader
    N1->>N2: Share Peer List
    N1->>N3: Share Peer List
    N2->>N3: Establish P2P Connection
    
    Note over N1,N3: P2P Network Formed
```

## 5. State Management and Content Advertisement

Shows how content availability is maintained and advertised in the P2P network, including periodic refresh cycles and event-driven updates.

```mermaid
sequenceDiagram
    participant ST as State Tracker
    participant CD as Containerd
    participant P2P as P2P Router
    participant DHT as DHT Network
    participant MT as Metrics

    Note over ST,DHT: Content TTL: 10 minutes
    Note over ST,DHT: Refresh: Every 9 minutes

    loop Every 9 minutes
        ST->>CD: List Images
        CD-->>ST: Image List
        
        loop For each image
            ST->>P2P: Advertise(image_keys)
            P2P->>DHT: Provide(keys)
        end
        
        ST->>MT: Update Metrics
    end

    CD-->>ST: Image Event (Create/Update/Delete)
    ST->>P2P: Update Advertisement
    ST->>MT: Update Metrics
```

## 6. Content Resolution Process

Shows how content is located and retrieved from peers in the network, including peer selection and retry mechanisms.

```mermaid
sequenceDiagram
    participant SR as Spegel Registry
    participant P2P as P2P Router
    participant DHT as DHT Network
    participant PR1 as Peer 1
    participant PR2 as Peer 2

    SR->>P2P: Resolve(content_key)
    P2P->>DHT: FindProviders(key)
    
    par Parallel Resolution
        DHT-->>P2P: Found Peer 1
        DHT-->>P2P: Found Peer 2
    end

    P2P->>SR: Return First Available Peer
    
    Note over SR,PR2: Default 20ms timeout
    Note over SR,PR2: 3 retry attempts
    
    alt Try Peer 1
        SR->>PR1: Request Content
        PR1-->>SR: Stream Content
    else Peer 1 Fails
        SR->>PR2: Request Content
        PR2-->>SR: Stream Content
    end
```

## 7. Data Flow Paths

Shows the content paths and system control flows, including peer transfers and fallback mechanisms.

```mermaid
graph LR
    subgraph "Content Paths"
        CD[Containerd]
        SP[Spegel]
        P[Peers]
        ER[External Registry]
        CS[Content Store]
        
        CD -->|Request| SP
        SP -->|Check| P
        P -->|Content| SP
        SP -->|Return| CD
        CD -->|Store| CS

        SP -->|404| CD
        CD -->|Fallback| ER
    end

    subgraph "P2P Operations"
        P2P[P2P Network]
        DHT[DHT]
        ST[State Tracker]
        
        P2P -->|Advertise| DHT
        DHT -->|Discover| P2P
        ST -->|Update| P2P
    end
```

## 8. Failure Handling

Shows how different types of failures are handled in the system.

```mermaid
sequenceDiagram
    participant CD as Containerd
    participant SR as Spegel Registry
    participant P2P as P2P Router
    participant PR as Peer
    participant ER as External Registry

    Note over SR,ER: Failure Scenarios

    alt Peer Not Found
        CD->>SR: Request Content
        SR->>P2P: Resolve(key)
        P2P--xSR: No Peers Available
        SR-->>CD: 404 Not Found
        CD->>ER: Fallback Request
    end

    alt Peer Connection Failed
        SR->>PR: Request Content
        PR--xSR: Connection Failed
        SR->>P2P: Resolve(key) Retry
        P2P-->>SR: Alternative Peer
    end

    alt Content Corrupted
        SR->>PR: Request Content
        PR-->>SR: Stream Content
        SR--xCD: Verification Failed
        CD->>ER: Fallback Request
    end
```

## 9. Metrics Collection

Shows how metrics are collected and organized across the system components.

```mermaid
graph TB
    subgraph "Metrics Sources"
        RQ[Registry Requests]
        P2P[P2P Operations]
        ST[State Changes]
    end

    subgraph "Metric Types"
        CT[Counters]
        HT[Histograms]
        GT[Gauges]
    end

    subgraph "Prometheus Metrics"
        MR[mirror_requests_total]
        RD[resolve_duration_seconds]
        AI[advertised_images]
        AK[advertised_keys]
        RL[request_latency]
        IF[requests_inflight]
    end

    RQ --> CT
    RQ --> HT
    P2P --> HT
    P2P --> GT
    ST --> GT

    CT --> MR
    HT --> RD
    HT --> RL
    GT --> AI
    GT --> AK
    GT --> IF
```