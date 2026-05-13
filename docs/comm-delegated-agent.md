# Communication Delegated to Agent Design Idea

We assume all communications delegated to agents are aiming at achieving certain tasks given by human.

Hence, it's important for the agent, whom granted permission, to focus and only focus on tasks assigned.

Tasks are given by human through raw prompts actively sent by human, not the final synthesised prompt sent to LLMs.

The agent should be able to store and track tasks given to them, potentially by using a markdown file stroing information as belows.
```
- [] task 1
    - [] task 1.1
    ...
- [] task 2
...
```

Using the above todo list style task tracking file, the agent should come back to understand if they have achieved all tasks (hence they can rest) and to understand if an attempt of doing certain things is necessary (by checking the list).