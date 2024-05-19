package visualize

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"

	"github.com/spegel-org/spegel/internal/mux"
	"github.com/spegel-org/spegel/pkg/oci"
)

// NOTE: image could be discoverd by peeking at the manifest content?
// TODO: When layer is not found it should default to subgraph for original registry

func Handler(ociClient oci.Client, store EventStore) http.Handler {
	handler := func(rw mux.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/visualize/":
			indexHandler(rw, req)
		case "/visualize/images":
			imagesHandler(rw, req, ociClient)
		case "/visualize/graph":
			graphHandler(rw, req, ociClient, store)
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
		border: 1px solid black;
		display: flex;
	}
  </style>
</head>

<body>
	<main>
		<div class="container">
		    <h1>Spegel</h1>
			<form hx-get="/visualize/graph" hx-trigger="load, change" hx-swap="none" hx-on::after-request="drawGraph(event)">
				<fieldset>
				    <legend>Request Direction</legend>

				    <input hx-preserve type="radio" id="incoming" name="direction" value="false" />
				    <label for="incoming">Incoming</label>

				    <input hx-preserve type="radio" id="both" name="direction" value="" checked />
				    <label for="both">Both</label>

				    <input hx-preserve type="radio" id="outgoing" name="direction" value="true" />
				    <label for="outgoing">Outgoing</label>
				</fieldset>

				<fieldset>
				    <legend>Images</legend>
				    <div hx-get="/visualize/images" hx-trigger="load, every 1s" hx-swap="innerHTML"></div>
				</fieldset>
			</form>
		    <div id="graph"></div>
			<script>
				const elem = document.getElementById('graph');
				const graph = ForceGraph()(elem);
				graph.width(1060)
					.height(700)
					.nodeId('id')
					.nodeLabel('id')
					.linkLabel('id')
					.linkColor('color')
					.linkCurvature('curvature')
					.linkDirectionalArrowRelPos(1)
					.linkDirectionalArrowLength(3);
				function drawGraph(event) {
					if (event.detail.pathInfo.requestPath != "/visualize/graph") {
						return
					}
					if (event.detail.successful != true) {
				        return console.error(event);
				    }
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
	rw.Write([]byte(index))
}

func imagesHandler(rw mux.ResponseWriter, req *http.Request, ociClient oci.Client) {
	imgs, err := ociClient.ListImages(req.Context())
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	tmpl, err := template.New("images").Parse(`
<style>
table {
	margin-top: 10px;
	width: 100%;
}
table, th, td {
	border: 1px solid black;
	border-collapse: collapse;
}
th, td {
	padding: 8px 5px;
}
th {
	text-align: left;
}
th:nth-child(3) {
  text-align: right;
}
td:nth-child(3) {
  text-align: right;
}
</style>
<table>
	<thead>
		<tr>
			<th></th>
			<th>Name</th>
			<th>Created</th>
		</tr>
	</thead>
	<tbody>
	{{ range $i, $element := . }}
		<tr>
			<td><input type="radio" hx-preserve id="{{ $i }}" name="image" value="{{ $element }}" {{ if eq $i 0 }}checked{{ end }} /></td>
			<td>{{ $element }}</td>
			<td>Today</td>
		</tr>
	{{ end }}
	</tbody>
</table>
	`)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	err = tmpl.Execute(rw, imgs)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
}

func graphHandler(rw mux.ResponseWriter, req *http.Request, ociClient oci.Client, store EventStore) {
	imgs, err := ociClient.ListImages(req.Context())
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	// Filter based on selected image
	imageFilter := req.URL.Query().Get("image")
	// TODO: Optimize with name lookup
	include := []string{}
	for _, img := range imgs {
		if img.String() != imageFilter {
			continue
		}
		ids, err := ociClient.AllIdentifiers(req.Context(), img)
		if err != nil {
			rw.WriteError(http.StatusInternalServerError, err)
			return
		}
		include = ids
		break
	}
	store = store.FilterById(include)

	// Filter based on direction
	directionFilter := req.URL.Query().Get("direction")
	if directionFilter != "" {
		isRootSource, err := strconv.ParseBool(directionFilter)
		if err != nil {
			rw.WriteError(http.StatusBadRequest, err)
			return
		}
		store = store.FilterByDirection(isRootSource)
	}

	gd := store.Graph()
	b, err := json.Marshal(&gd)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
	}
	rw.Write(b)
}
