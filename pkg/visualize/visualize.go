package visualize

import (
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"strconv"

	"github.com/spegel-org/spegel/internal/mux"
)

func Handler(store EventStore) http.Handler {
	handler := func(rw mux.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/visualize/":
			indexHandler(rw, req)
		case "/visualize/graph":
			graphHandler(rw, req, store)
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	}
	return mux.NewServeMux(handler)
}

func indexHandler(rw mux.ResponseWriter, _ *http.Request) {
	index := `
<!DOCTYPE html>
<html lang="en">
<head>
  <title>Spegel</title>
  <script src="https://unpkg.com/htmx.org@1.9.12"></script>
  <script src="https://unpkg.com/force-graph@v1.43.5"></script>
  <link rel="icon" href="data:,">
  <link href='https://fonts.googleapis.com/css?family=Open Sans' rel='stylesheet'>
  <style>
	body {
	    margin: 0;
	    font-family: 'Open Sans';
	    font-size: 16px;
	}
	main {
		margin: 0 auto;
	    max-width: 1060px;
	    width: 100%;
	}
	fieldset {
		margin-bottom: 10px;
	}
	.container {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	#graph {
		margin: 0 2px;
		height: 700px;
		border-width: 2px;
		border-style: groove;
		border-color: rgb(192, 192, 192);
		border-image: initial;
	}
  </style>
</head>

<body>
	<main>
		<div class="container">
		    <h1>Spegel</h1>
			<form hx-get="/visualize/graph" hx-trigger="load, change, every 2s" hx-swap="none" hx-on::after-request="drawGraph(event)">
				<fieldset>
				    <legend>Request Direction</legend>

				    <input hx-preserve type="radio" id="incoming" name="direction" value="false" />
				    <label for="incoming">Incoming</label>

				    <input hx-preserve type="radio" id="both" name="direction" value="" checked />
				    <label for="both">Both</label>

				    <input hx-preserve type="radio" id="outgoing" name="direction" value="true" />
				    <label for="outgoing">Outgoing</label>
				</fieldset>
			</form>
		    <div id="graph"></div>
			<script>
				const elem = document.getElementById('graph');
				const graph = ForceGraph()(elem);
				graph.nodeId('id')
					.width(elem.clientWidth)
					.height(elem.clientHeight)
					.nodeLabel('id')
					.nodeColor((n) => {
						if (n.id == "self") {
							return 'rgba(166, 166, 168, 1)'	
						}
						return 'rgba(0, 109, 170, 1)'	
					})
					.linkLabel('id')
					.linkColor((n) => {
						switch (n.status) {
							case 0:
								return "yellow";
							case 200:
								return "green";
							default:
								return "red";
						}
					})
					.linkCurvature('curvature')
					.linkDirectionalArrowRelPos(1)
					.linkDirectionalArrowLength(2);
				var etag = ""
				function drawGraph(event) {
					if (event.detail.pathInfo.requestPath != "/visualize/graph") {
						return
					}
					if (event.detail.successful != true) {
				        return console.error(event);
				    }
				    let newEtag = event.detail.xhr.getResponseHeader("etag")
				    if (etag == newEtag) {
				    	return
				    }
				    etag = newEtag
				    let data = JSON.parse(event.detail.xhr.response)
				    // Compute the curvature for links sharing the same two nodes to avoid overlaps
				    let sameNodesLinks = {};
				    const curvatureMinMax = 0.5;
				    data.links.forEach(link => {
				      link.nodePairId = link.source <= link.target ? (link.source + "_" + link.target) : (link.target + "_" + link.source);
				      let map = link.source === link.target ? selfLoopLinks : sameNodesLinks;
				      if (!map[link.nodePairId]) {
				        map[link.nodePairId] = [];
				      }
				      map[link.nodePairId].push(link);
				    });
				    Object.keys(sameNodesLinks).filter(nodePairId => sameNodesLinks[nodePairId].length > 1).forEach(nodePairId => {
				      let links = sameNodesLinks[nodePairId];
				      let lastIndex = links.length - 1;
				      let lastLink = links[lastIndex];
				      lastLink.curvature = curvatureMinMax;
				      let delta = 2 * curvatureMinMax / lastIndex;
				      for (let i = 0; i < lastIndex; i++) {
				        links[i].curvature = - curvatureMinMax + i * delta;
				        if (lastLink.source !== links[i].source) {
				          links[i].curvature *= -1; // flip it around, otherwise they overlap
				        }
				      }
				    });
				    graph.graphData(data)
				}
			</script>
		</div>
	</main>
</body>

</html>
`
	//nolint: errcheck // Ignore error.
	rw.Write([]byte(index))
}

func graphHandler(rw mux.ResponseWriter, req *http.Request, store EventStore) {
	directionFilter := req.URL.Query().Get("direction")
	if directionFilter != "" {
		isRootSource, err := strconv.ParseBool(directionFilter)
		if err != nil {
			rw.WriteError(http.StatusBadRequest, err)
			return
		}
		store = store.FilterByDirection(isRootSource)
	}
	eTagValue := directionFilter + "-" + store.LastModified().String()
	hash := fnv.New32a()
	_, err := hash.Write([]byte(eTagValue))
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	eTag := hex.EncodeToString(hash.Sum(nil))
	if eTag == req.Header.Get("If-None-Match") {
		rw.WriteHeader(http.StatusNotModified)
		return
	}
	rw.Header().Set("etag", eTag)
	gd := store.Graph()
	b, err := json.Marshal(&gd)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	//nolint: errcheck // Ignore error.
	rw.Write(b)
}
