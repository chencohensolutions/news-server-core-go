# Narrative vectors

A story that runs for months (a war, an investigation, coalition talks) comes back in pieces: the grouping only sees
its last `story_days`, so each return is a new story, and the thread linking used to find its affair by showing
Gemini every thread (up to 3,000) in each request. The narrative vectors give the server a memory of meaning across
the whole archive, with the story model it already runs (bge-m3, local, free).

## What is kept

- **A story:** its title and first two sentences (about what the model reads in 128 tokens), embedded when it is kept
  and again when its text changes (a hash of the text is kept beside the vector).
- **A thread:** the mean of its stories' vectors, each weighted by its age at the thread's latest story, halving
  every 30 days (`narrative.HalfLife`), so a thread that changed course points where it is now. Made again when a
  story joins it.

They are in news.db, for good: the rows in `narratives`, the vectors in a sqlite-vec `vec0` table per vector size
(`narrative_vec_1024` for bge-m3), searched by cosine. sqlite-vec comes with `modernc.org/sqlite/vec` (no cgo, Windows
included); a search over 50,000 vectors takes about 100 ms and holds nothing in memory. The archive folder doesn't keep
them: after a restore, `news-server narratives` makes them again from the stories.

The headline vectors the grouping uses (`story_vectors`) still last only the grouping's days: a million headlines a
year at 4 KB each would be gigabytes, and the stories are the unit a narrative is made of.

## The linking

Before each linking the server embeds the stories and threads that changed. For each new story the request lists:

- the 10 threads nearest to it (`thread_candidates`),
- the threads of the 10 earlier stories nearest to it,
- the 20 latest updated threads (what is going on now),

latest updated first, in place of every thread; and the earlier stories near it (similarity 0.6 or more, up to 3)
that are in no thread. Gemini may put such an earlier story in a new thread with the new story that continues it:
the thread then starts at its first chapter. An earlier story joins only a thread a new story is in.

`thread_candidates: -1` lists every thread as before. Without the story model, or before any story has a vector,
the linking lists every thread too.

## Measuring

`news-server narratives` embeds what is missing (the first time, every kept story: minutes on the PC), then takes the
latest 500 stories that continue a thread Gemini linked them to while seeing every thread, and checks whether the
shortlist, built from what existed before each story, would have listed one of its threads. It prints the recall and
the threads listed for the thread vectors alone (no weighting, and half-lives of 7, 30 and 90 days), the earlier
stories' threads alone, both, and both with the latest threads (what the linking does).
