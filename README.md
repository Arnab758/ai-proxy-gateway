# AI Gateway

**Cut your LLM API costs by 40-70% with zero code changes.**

[![Open Live Demo](https://img.shields.io/badge/🎮_Try_Live_Demo-Now-blue?style=for-the-badge)](https://ai-gateway-production-c86a.up.railway.app/demo)

A semantic caching layer that sits between your app and AI providers (OpenAI, Groq, etc.). When you ask a similar question twice, it returns the cached answer instantly instead of calling the API again.

## 🎯 What Problem Does This Solve?

You're building an AI app and your API bill is $500/month. 40-70% of that is for **repeat questions**:
- "What is RAG?" asked 100 times = 100 API calls
- "How do I reset my password?" asked 50 times = 50 API calls

**With AI Gateway:** Those 150 calls become 2 calls (one for each unique question). You save $200-350/month.

## 💬 Feedback & Support

**How was your deployment experience?**

[![Give Feedback](https://img.shields.io/badge/📝_Share_Feedback-Here-green?style=for-the-badge)](https://github.com/Arnab758/ai-gateway/discussions)

*Takes 30 seconds. Helps us improve AI Gateway for everyone.*

**What we want to know:**
- ⭐ How did deployment go? (Excellent / Average / Bad)
- 🐛 Any problems you faced?
- 💡 What features would you like to see?
- 📊 How much are you saving on API costs?

**Your feedback directly shapes the roadmap.**

---

## 🚀 Deploy in 60 Seconds (3 Options)

### Option 1: Railway (Recommended - Includes Redis)

[![Deploy to Railway](https://railway.app/button.svg)](https://railway.app/direct?template=https://github.com/Arnab758/ai-gateway&envs=UPSTREAM_API_KEY&UPSTREAM_API_KEY=your_groq_or_openai_api_key)

**Steps:**
1. Click the button above
2. Sign in with GitHub
3. Enter your API key (Groq or OpenAI)
4. Click "Deploy"
5. Done! Your gateway is live at `https://your-app.up.railway.app`

**What you get:**
- ✅ Hosted gateway (no server management)
- ✅ Redis included (persistent cache)
- ✅ Auto-scaling
- ✅ HTTPS enabled
- ✅ $5/month free credit

### Option 2: Render (One-Click Deploy)

[![Deploy to Render](https://render.com/images/deploy-to-render-button.svg)](https://render.com/deploy?repo=https://github.com/Arnab758/ai-gateway)

**Steps:**
1. Click the button
2. Sign in with GitHub
3. Add environment variable: `UPSTREAM_API_KEY=your_key`
4. Click "Create Web Service"
5. Done!

**Note:** You'll need to add a Redis addon separately in Render dashboard.

### Option 3: Docker (Self-Hosted)

**Prerequisites:**
- Docker installed
- Docker Compose installed
- A Groq or OpenAI API key

**Steps:**

```bash
# 1. Clone the repo
git clone https://github.com/Arnab758/ai-gateway.git
cd ai-gateway

# 2. Set your API key
export UPSTREAM_API_KEY=gsk_your_groq_key_here

# 3. Start everything (gateway + Redis)
docker compose up -d

# 4. Verify it's running
curl http://localhost:8080/health

# Expected response: {"status":"ok"}
```

**That's it!** Your gateway is now running at `http://localhost:8080`


## 📊 Network Analytics (Phone Home)

AI Gateway includes an optional analytics system to track usage across all deployments. This data helps improve the project and demonstrates real-world traction.

### How It Works

- **Default: ON** - Every deployment sends basic usage stats every 24 hours
- **No sensitive data** - Only anonymous counts are sent (no prompts, API keys, or personal info)
- **Aggregated dashboard** - See stats from all deployments

**Disable with:** `DISABLE_ANALYTICS=true` environment variable

### What Gets Tracked

```
✓ Total requests processed
✓ Cache hits vs misses (counts only)
✓ Estimated tokens saved
✓ Estimated cost savings
✓ Number of unique tenants (app names only)
```

### View Your Gateway's Stats

```bash
curl https://your-gateway-url.com/api/analytics
```

Response:
```json
{
  "local": {
    "uptime_seconds": 86400,
    "total_requests": 1500,
    "cache_hits": 1050,
    "cache_misses": 450,
    "hit_rate_percent": 70.0,
    "tokens_saved": 52500,
    "estimated_savings": "$1.5750",
    "tenant_count": 3
  },
  "network": {
    "endpoint": "/api/network-stats"
  }
}
```

### View Global Network Stats

```bash
curl https://ai-gateway-production-c86a.up.railway.app/api/network-stats
```

This shows aggregated data from all deployments that have analytics enabled.

### Let Us Know You're Using It

After deploying, help us track active deployments:

```bash
curl https://your-gateway-url.com/api/deployed \
  -H "X-Gateway-Token: your-app-name"
```

---

## 📖 How to Use

### Basic Usage (cURL)

```bash
# Send a request through the gateway
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Gateway-Token: my-app" \
  -H "Authorization: Bearer sk-your-openai-or-groq-key" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "What is RAG?"}]
  }'

# Send the SAME request again
# Response headers will show: X-Gateway-Cache: HIT
# You just saved money! 💰
```

### Python Example

```python
import requests

# Your gateway URL (from Railway/Render/Docker)
GATEWAY_URL = "https://your-app.up.railway.app"
API_KEY = "sk-your-key"

response = requests.post(
    f"{GATEWAY_URL}/v1/chat/completions",
    headers={
        "Content-Type": "application/json",
        "X-Gateway-Token": "my-app",
        "Authorization": f"Bearer {API_KEY}"
    },
    json={
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "What is RAG?"}]
    }
)

print(response.json())
```

### Node.js Example

```javascript
const response = await fetch('https://your-app.up.railway.app/v1/chat/completions', {
  method: 'POST',
  headers: {
    'Content-Type': 'application/json',
    'X-Gateway-Token': 'my-app',
    'Authorization': 'Bearer sk-your-key'
  },
  body: JSON.stringify({
    model: 'gpt-4',
    messages: [{ role: 'user', content: 'What is RAG?' }]
  })
});

const data = await response.json();
console.log(data);
```

## 🔥 Key Features

- **Semantic Caching** - Matches similar questions, not just exact duplicates
  - "What is RAG?" = "Explain RAG" = "RAG definition"
- **Multi-Tenant** - Each customer gets their own isolated cache
- **4-Tier Matching:**
  1. Exact match (100% identical)
  2. Template match ("weather in London" = "weather in Paris")
  3. Semantic match (similar meaning)
  4. Word overlap (partial matches)
- **Redis + In-Memory Fallback** - Works with or without Redis
- **Request Deduplication** - 100 concurrent identical requests = 1 API call
- **Rate Limiting** - Prevent abuse per tenant
- **Circuit Breaker** - Automatically stops calling if provider is down
- **Cost Tracking** - See how much you saved

## 📊 Real-World Example

**Scenario:** Customer support chatbot with 10,000 users

**Without AI Gateway:**
- 10,000 users ask 100 common questions each
- 1,000,000 API calls/month
- Cost: $500/month (at $0.0005/call)

**With AI Gateway:**
- First 100 questions: 100 API calls (cache miss)
- Next 9,900 users asking same questions: 0 API calls (cache hit)
- Total: 100 API calls/month
- Cost: $0.05/month
- **Savings: $499.95/month (99.99%)**

**Even with 30% unique questions:**
- 300,000 API calls
- Cost: $150/month
- **Savings: $350/month (70%)**

## 🛠️ Configuration

Edit `gateway.yaml` to customize:

```yaml
cache:
  redis_url: "redis://localhost:6379"  # Or your Redis URL
  vector:
    enabled: true
    similarity_threshold: 0.85  # 85% similar = cache hit
  ttl_hours: 24  # Cache entries expire after 24 hours

rate_limiter:
  enabled: true
  max_requests: 60  # Per minute per tenant
```

## 📡 API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/chat/completions` | POST | Main proxy endpoint with caching |
| `/health` | GET | Health check |
| `/stats` | GET | Cache statistics |
| `/metrics` | GET | Prometheus metrics |

## 🔍 Monitoring

### Check Cache Stats

```bash
curl http://localhost:8080/stats
```

Response:
```json
{
  "uptime": 1234567890,
  "cache": {
    "local_index_entries": 150,
    "vector_dimensions": 128,
    "vector_threshold": 0.85,
    "jaccard_threshold": 0.75,
    "template_enabled": true,
    "dedup_enabled": true,
    "ttl_hours": 24
  }
}
```

### Response Headers

Every response includes cache information:

```
X-Gateway-Cache: HIT          # or MISS
X-Gateway-Similarity: 0.95    # 95% similar (if HIT)
X-Gateway-Time-Saved: 1234ms  # Time saved (if HIT)
```

## 🐛 Troubleshooting

### Problem: "Redis connection failed"

**Solution:** Redis is optional! The gateway will fall back to in-memory cache automatically. For production, add Redis:

**Railway:** Add Redis from the "New" button
**Render:** Add Redis from the "New" → "Database" → "Redis"
**Docker:** Already included in `docker-compose.yml`

### Problem: "All upstream providers unavailable"

**Cause:** You're hitting rate limits on free tier (Groq/OpenAI)

**Solutions:**
1. Wait 1-2 minutes and try again
2. Upgrade to paid tier ($0.002/request vs free limits)
3. Add your own API key with higher limits

### Problem: "Rate limit exceeded"

**Cause:** Too many requests from one tenant

**Solution:** Increase rate limits in `gateway.yaml`:
```yaml
rate_limiter:
  max_requests: 120  # Increase from 60
  window_minutes: 1
```

### Problem: Cache not hitting

**Cause:** Prompts are too different

**Solution:** Lower the similarity threshold in `gateway.yaml`:
```yaml
cache:
  vector:
    similarity_threshold: 0.75  # Lower from 0.85
  jaccard:
    threshold: 0.65  # Lower from 0.75
```

## 🛡️ Production: Zero-Downtime Setup

For production applications, we recommend implementing failover in your application code. This ensures your app never goes down, even if the gateway has issues.

### Python Example

```python
import requests

def chat_with_fallback(messages, use_gateway=True):
    if use_gateway:
        try:
            # Try gateway first (cached, cheaper)
            response = requests.post(
                "https://your-gateway.up.railway.app/v1/chat/completions",
                headers={
                    "Content-Type": "application/json",
                    "Authorization": "Bearer sk-your-key",
                    "X-Gateway-Token": "my-app"
                },
                json={"model": "gpt-4", "messages": messages},
                timeout=3  # 3 second timeout
            )
            if response.status_code == 200:
                return response.json()
        except:
            # Gateway failed, fallback to direct LLM call
            pass
    
    # Fallback: Call LLM directly (always works)
    response = requests.post(
        "https://api.openai.com/v1/chat/completions",
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer sk-your-key"
        },
        json={"model": "gpt-4", "messages": messages}
    )
    return response.json()
```

### Node.js Example

```javascript
async function chatWithFallback(messages, useGateway = true) {
  if (useGateway) {
    try {
      const response = await fetch('https://your-gateway.up.railway.app/v1/chat/completions', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer sk-your-key',
          'X-Gateway-Token': 'my-app'
        },
        body: JSON.stringify({
          model: 'gpt-4',
          messages: messages
        }),
        signal: AbortSignal.timeout(3000) // 3 second timeout
      });
      
      if (response.ok) {
        return await response.json();
      }
    } catch (error) {
      // Gateway failed, fallback to direct
    }
  }
  
  // Fallback: Call LLM directly
  const response = await fetch('https://api.openai.com/v1/chat/completions', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': 'Bearer sk-your-key'
    },
    body: JSON.stringify({
      model: 'gpt-4',
      messages: messages
    })
  });
  
  return await response.json();
}
```

**Benefits:**
- ✅ Gateway works = you save 40-70% on API costs
- ✅ Gateway fails = app still works (no downtime)
- ✅ Zero risk to your production application
- ✅ Best of both worlds

---

## 🏗️ Architecture

```
Your App → AI Gateway → [Cache Check] → Redis
                ↓
            [Cache HIT] → Return cached response (instant, $0)
                ↓
            [Cache MISS] → Call LLM Provider → Cache response → Return
                ↓
        [Gateway Down] → App automatically calls LLM directly
```

## 🤝 Contributing

Contributions are welcome! Please:

1. Fork the repo
2. Create a feature branch
3. Make your changes
4. Submit a pull request

## 📄 License

MIT License - feel free to use this commercially!

## 🙋 Support

- **Issues:** [GitHub Issues](https://github.com/Arnab758/ai-gateway/issues)
- **Discussions:** [GitHub Discussions](https://github.com/Arnab758/ai-gateway/discussions)
- **Demo:** [Live Demo](https://ai-gateway-production-c86a.up.railway.app/demo)

## ⭐ Star History

If this project helps you, please give it a star! It helps others find it.

---

**Built with ❤️ for the AI community**

**Questions?** Open an issue and I'll respond within 24 hours.