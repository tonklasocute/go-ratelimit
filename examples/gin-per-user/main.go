// Command gin-per-user gives each authenticated user a limit that depends on
// their plan, and limits anonymous clients by IP.
//
//	go run ./gin-per-user
//	curl -i -H 'Authorization: Bearer alice' localhost:8080/api/items   # free plan
//	curl -i -H 'Authorization: Bearer bob'   localhost:8080/api/items   # pro plan
//	curl -i localhost:8080/api/items                                    # anonymous
package main

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	ratelimit "github.com/tonklasocute/go-ratelimit"
	rlgin "github.com/tonklasocute/go-ratelimit/middleware/gin"
	"github.com/tonklasocute/go-ratelimit/store/memory"
)

// plans maps users to plans. A real app would read this from its database.
var plans = map[string]string{"alice": "free", "bob": "pro"}

func main() {
	store := memory.New()
	defer store.Close()

	// Each plan combines a short burst rule and an hourly quota. The rule
	// names keep the plans' counters apart in the shared store.
	newPlan := func(name string, perSecond, perHour int) gin.HandlerFunc {
		lim, err := ratelimit.NewComposite([]ratelimit.Rule{
			{Name: name + "_burst", Rate: ratelimit.PerSecond(perSecond)},
			{Name: name + "_hourly", Rate: ratelimit.PerHour(perHour), Algorithm: ratelimit.SlidingWindow},
		}, ratelimit.WithStore(store))
		if err != nil {
			log.Fatal(err)
		}
		return rlgin.New(lim, rlgin.WithKeyFunc(rlgin.KeyByContextValue("user")), rlgin.WithJSONBody())
	}
	limits := map[string]gin.HandlerFunc{
		"free": newPlan("free", 2, 100),
		"pro":  newPlan("pro", 20, 10_000),
	}

	anonymous, err := ratelimit.New(ratelimit.PerMinute(10), ratelimit.WithName("anonymous"), ratelimit.WithStore(store))
	if err != nil {
		log.Fatal(err)
	}
	limitAnonymous := rlgin.New(anonymous, rlgin.WithJSONBody())

	r := gin.Default()
	api := r.Group("/api", authenticate, func(c *gin.Context) {
		if plan, ok := c.Get("plan"); ok {
			limits[plan.(string)](c)
			return
		}
		limitAnonymous(c)
	})
	api.GET("/items", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"items": []string{"a", "b"}, "user": c.GetString("user")})
	})

	log.Fatal(r.Run(":8080"))
}

// authenticate is a stand-in for real authentication: "Bearer <user>".
func authenticate(c *gin.Context) {
	user, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	if !ok {
		return
	}
	plan, known := plans[user]
	if !known {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	c.Set("user", user)
	c.Set("plan", plan)
}
