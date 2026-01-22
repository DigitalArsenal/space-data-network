package web

import (
	"net/http"

	"github.com/DigitalArsenal/space-data-network/internal/node"
	"github.com/gin-gonic/gin"
)

// API represents the HTTP API layer for interacting with a Node
type API struct {
	Node *node.Node
}

// NewAPI creates a new API instance with a reference to a Node
func NewAPI(node *node.Node) *API {
	return &API{
		Node: node,
	}
}

// Start initializes the HTTP server and routes
func (api *API) Start() {

}

// SpaceDataPost handles POST requests for CCSDS messages
// @Summary Post CCSDS message
// @Accept json
// @Produce json
// @Success 200 {string} status "ok"
// @Router /ccsds [post]
func (api *API) SpaceDataPost(c *gin.Context) {
	// Parse and handle the CCSDS message here
	// Use your heuristic function to determine the format (KVN, XML, CSV, JSON)

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// handleCCSDSGet handles GET requests for CCSDS messages
// @Summary Get CCSDS message
// @Produce json
// @Success 200 {string} status "ok"
// @Router /ccsds [get]
func (api *API) handleCCSDSGet(c *gin.Context) {
	// Retrieve and send the CCSDS message here
	// The message could be in any format (KVN, XML, CSV, JSON)

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
