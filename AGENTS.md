## SOURCE OF TRUTH for project

Whenever you need to "Let me first understand the current source code before implementing." -> use "research database" inside ./docs/\*.md, DO NOT READ OTHER SOURCES if you have not researched database

DO NOT READ ALL THE DB EACH TIME . Just read relevant files

If you did some structural changes worth writing to this research database -> write changes, create new files if necessary .

NEVER, under any circumstances read a file if you already know something about a feature/thing from md file.
ONLY in a case of lack of information in "research database" read files, research and

ALWAYS write down research results into this db under specific md file(s):

- If no research topic is present in knowledge base create new one
- Try to separate general info , like backend.md and auth.system.md.
- One research might update general knowledge and more specific feature at once.

WHAT research database IS NOT: its not a changelog - it should reflect last state of codebase.
Whenever you changes somthing , only the last state should be in database, old docs need to be cleaned

Store timestamps on changes to know how old those changes / state are.

For any temporal work use ./tmp folder inside this project, not system /tmp.
