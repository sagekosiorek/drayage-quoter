# Outline
**The Challenge**

Quoting drayage is currently handled manually. Vendor networks are informally kept and selected. While there'll always be a lag between the time reps blast out rate requests and vendor responses, this opportunity is concerned with the system used on either end - front-end lane logging, blasting to the correct carriers, organizing rates that come in, selecting the appropriate carrier to use along with a few backups, marking up the rate, and sending to the prospect. This process done manually can take anywhere from 10 - 20 min per lane. Excluding the time it takes for vendors to respond.

To put this into perspective, by quoting on average 4 lanes per day, we're talking 40 min - 1 hr 20 min per day, 3 - 6 hours per week, or 12 - 24 hours per month spent quoting. And while there's  substantial intellectual time spent analyzing how best to quote, the key opportunity here is reducing the 50 - 80% time spent doing route tasks, further freeing up sale's reps intellectual capacity to quote well.

**Business Impact**

Buy incorporating an end-to-end internal quoting system for Drayage, we forsee a 30% - 50% reduction in time spent quoting per lane, allowing sales reps to focus on pure analysis.

Not only will this save significant time, but also directly impact revenue given sales rep's increased mental capacity during the analysis process. Given the time pressure prospects assert on sales reps, and by reducing the number of rote tasks required per quote, we expect a night and day difference in not only the rep's psychological satisfaction, but qualitatively sharper quotes.

**Proposed Solution**

A web application that facilitates the end-to-end Drayage quoting process, automating rote tasks at each step. It includes a vendor database, shared among reps, with persisted notes to quickly reference carrier qualifications by lane. Rate requests are sent out via email - coming from the reps email address - by port and/or rail head and only carriers who service the area receive it. They respond over email which routes directly back to the rep's inbox, which is then forwarded to this application for automatic formatting for easy comparison.

Once a satisfactory number of responses arrive, the rep will receive an email notification that the rates are ready for comparison. They then see the rates aggregated into a simple table for analysis (including accessorial rates). Within the name interface, they have reference to the original email in case parts of the vendor quote is not clear. They can then easily select a primary, secondary, n-ary carrier to base their prospect's quote off of it.

Markups are done in a clean, easy-to-use interface. Then once ready, they can download a pre-formatted CSV file to send to the customer directly.

**Approach**
Drayage sales reps use the following tech stack currently. The Drayage Quoter web app will exist separately, and integrated with the existing tool set where possible.

Current tools:
- Email for vendor rate request blasts, vendor responses, and ongoing communications
- Manual reading of rates, sifting through each vendor-specific grammar and formatting to determine otherwise standard charge types
- Excel sheets for quote tracking, markup analysis, customer-facing quote formatting, customer rate tracking, and vendor network state and blasts

New stack to support this will include:
- Go backend for application and web core, including APIs and trigger points for email integration.
- HTMX for simple server-side rendered frontends (no complex rendering needed).
- SQLite for database service to support lane, rate, and vendor data.
- CSV and PDF file generator to support file-first principle
- Lua for scripting integrations with email (likely)
- HTTP for communication between systems
- Fly.io + Docker for containerized deployment

You'll note that Excel has been removed from the picture entirely, while email remains central. Excel was the existing solution, but given fairly standard sales rep's workflows a standard system should step in to fill the gap. The goal of a standardized system like this is to boost everyone's productivity, not just those who are technical enough to automate their own workflows.

---

# Milestones

## 1: Log lane details and quotes
When a new opportunity to quote comes in, a sales rep can simply log the comprehensive lane details in a clean, easy-to-use interface. It maintains a list of past lane opportunities, effectively serving as a running opportunity list.

[This is a dummy approach because the better option would be to simply integrate with MasterMind, leveraging its opportunities feature within the CRM. A natural extension, this tool would simply aggregate those opportunities into a single, running list. Like my pricing sheet.]

## 2: Log and organize vendor network
Sales reps can log vendor details such as points of contact, ports serviced, and reputation. Each new vendor is logged in a master list shared among the drayage team, but sales reps can select preferred vendors by port serviced to build out their own sub-network they trust.

## 3: Blast select vendors based on lane details
Once the lane opportunities and vendor network is established, a rep can easily request rates from their preferred vendor lists. They can also determine how many vendor responses they want before being notified via email that their rates are ready for comparison.

## 4: Analyze vendor rates
Once a rep receives notification, they open the Drayage Quoter and see that their rate request form has transformed into a table organized by vendor (sorted by cheapest to highest LH + Fuel) along with all accessorials parsed from the original emails. If certain accessorial rates don't look right, they can check the original email by clicking a button that renders it next to the table. They can modify any automatically parsed rates as they see fit.

## 5: Create service provider "lineup"
Reps can select `n` number of vendors in sequence to create a "lineup" of service providers for a given lane. They have the ability to change this on the fly, and in the future.

## 6: Log markup and generate a quote
Finally, the rep can easily mark up any number of rates associated with a lane. They have the option to enter an exact amount, incremental, or percentage based. Once sufficient, they can generate a pre-formatted CSV file they can use to send to the customer.

---
# Timeline and investment
Executed under a Tier 3 engagement level

| **Estimated Development Time** | 47 - 93 hours                                                                                                                                              |
| ------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Estimated Investment**       | $7,050 - $13,950                                                                                                                                           |
| **Expected ROI**               | 3.6 - 12 hours per month per rep saved during quoting process; qualitatively sharper quotes for every prospective client, directly impacting closure-rate. |

---

# ROI Metrics
_To be completed when the system is in production._

**Quantitative (Hard Numbers)**
_Can calculate within 10-20 days post-deployment_

| Category           | Example Metrics                                                                     |
| ------------------ | ----------------------------------------------------------------------------------- |
| **Time Saved**     | Hours/week recovered, FTE equivalent, labor cost reduction (hours × rate)           |
| **Revenue Impact** | Faster sales cycles, improved close rates, reduced churn, capacity for new business |
| **Cost Reduction** | Error/rework costs, tool consolidation, avoided hires                               |
| **Speed**          | Cycle time (quote-to-close, order-to-ship), response time, throughput               |
| **Error/Quality**  | Error rate, rework frequency, data accuracy, compliance incidents                   |

**Semi-Quantitative (Operational)**
_Can calculate within 60-90 days post-deployment_

| Category           | Example Metrics                                                 |
| ------------------ | --------------------------------------------------------------- |
| **Capacity**       | Volume handled without adding headcount, bottleneck elimination |
| **Risk Reduction** | Single points of failure removed, key-person dependency reduced |
| **Visibility**     | Time to access critical data, decision confidence               |

**Qualitative (Psychological / Experience)**
_Harder to measure but real (reduced stress, better employee retention, customer trust)_

| Category                | What It Looks Like                                              |
| ----------------------- | --------------------------------------------------------------- |
| **Cognitive load**      | Fewer things to "keep in your head," reduced context-switching  |
| **Stress/anxiety**      | Confidence nothing's slipping through cracks, less firefighting |
| **Employee experience** | Less tedious work, reduced burnout, higher job satisfaction     |
| **Customer experience** | Faster responses, fewer dropped balls, more consistency         |
