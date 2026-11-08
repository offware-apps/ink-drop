package io.github.offwareapps.inkdrop;

import java.util.Map;

// RulesTest checks the app's own rules on the laptop, during the build: what
// names a book's folder and section, and how a row marks its source. A rule
// that fails stops the build.
public final class RulesTest {
  private static int failed;

  private static void want(String what, String got, String want) {
    if (!got.equals(want)) {
      System.err.println("FAIL " + what + ": got \"" + got + "\", want \"" + want + "\"");
      failed++;
    }
  }

  public static void main(String[] args) {
    for (Map.Entry<String, String> e : Map.of(
        "https://www.lemonde.fr/politique/article/2026/11/07/x.html", "Politique",
        "https://www.franceinfo.fr/faits-divers/en-pologne_8272454.html#xtor=RSS-3", "Faits divers",
        "https://go.dev/blog/generic-methods", "",
        "https://example.org/2026/11/07/post", "",
        "https://example.org/", "",
        "https://example.org/a.b/c", "",
        "not a url", "",
        "https://example.org/article/x", "",
        "https://example.org/laptop/x", "").entrySet()) {
      want("section of " + e.getKey(), Refresher.section(e.getKey()), e.getValue());
    }
    for (Map.Entry<String, String> e : Map.of(
        "Le Monde.fr - Actualités et Infos en France", "Le Monde.fr",
        "Franceinfo | Les Titres", "Franceinfo",
        "Laptop", "Laptop feed",
        "pages", "pages feed",
        "../../etc", "Feed",
        "a/b\\c:d", "a b c d",
        "", "Feed",
        ".hidden", "Feed").entrySet()) {
      want("shelf of " + e.getKey(), Refresher.shelf(e.getKey()), e.getValue());
    }
    want("a shelf never holds a path separator", String.valueOf(Refresher.shelf("x/../y").contains("/")), "false");
    want("initials of Le Monde.fr", MainActivity.initials("Le Monde.fr"), "LM");
    want("initials of Franceinfo", MainActivity.initials("Franceinfo"), "FR");
    want("initials of Pages", MainActivity.initials("Pages"), "");
    want("initials of Laptop", MainActivity.initials("Laptop"), "");
    want("mark of Politique", MainActivity.mark("Le Monde.fr", "Politique"), "🏛");
    want("mark of Idées", MainActivity.mark("Le Monde.fr", "Idées"), "💡");
    want("mark of an unknown section", MainActivity.mark("Franceinfo", "Replay"), "📰");
    want("mark of a shared page", MainActivity.mark("Pages", ""), "📄");
    want("mark of a laptop book", MainActivity.mark("Laptop", "reviews"), "💻");
    want("a Wikipedia title keeps its parenthesis", MainActivity.trimShared("https://en.wikipedia.org/wiki/Python_(programming_language)"), "https://en.wikipedia.org/wiki/Python_(programming_language)");
    want("a sentence's closing marks go", MainActivity.trimShared("https://example.org/a)."), "https://example.org/a");
    want("both at once", MainActivity.trimShared("https://en.wikipedia.org/wiki/Mercury_(planet))."), "https://en.wikipedia.org/wiki/Mercury_(planet)");
    String root = "primary:Books/ink-drop";
    want("Laptop by its path", String.valueOf(Refresher.underLaptop(root + "/Laptop/reviews/x.epub")), "true");
    want("the Laptop folder itself", String.valueOf(Refresher.underLaptop(root + "/Laptop")), "true");
    want("a folder picked inside Laptop", String.valueOf(Refresher.underLaptop(root + "/Laptop/reviews")), "true");
    want("Laptop under a parent picked", String.valueOf(Refresher.underLaptop("primary:Books/ink-drop/laptop/x.epub")), "true");
    want("a feed named like it", String.valueOf(Refresher.underLaptop(root + "/Laptop feed/x.epub")), "false");
    want("a feed's book", String.valueOf(Refresher.underLaptop(root + "/Franceinfo/x.epub")), "false");
    want("ink-drop picked", String.valueOf(Refresher.underLaptop(root)), "false");
    want("shelves without case", String.valueOf(Refresher.sameShelf("World news", "World News")), "true");
    want("shelves that differ", String.valueOf(Refresher.sameShelf("World news", "World")), "false");
    want("no shelf", String.valueOf(Refresher.sameShelf(null, "World")), "false");
    want("a sync folder in lower case keeps its mark", MainActivity.mark("laptop", ""), "💻");
    want("and has no initials", MainActivity.initials("laptop"), "");
    want("a refresh's one book has no section", Refresher.section("https://example.org/politics/feed#digest-20261108T100000Z"), "");
    want("an article's fragment is no digest", Refresher.section("https://example.org/politics/x#top"), "Politics");
    long day = 86_400_000L, now = 100 * day;
    long[] times = {now - 1000, now - 2 * day, now - 8 * day, now - 9 * day};
    want("a week drops what is older", Refresher.past(times, now, 7, 0).toString(), "[2, 3]");
    want("a day drops all but the newest", Refresher.past(times, now, 1, 0).toString(), "[1, 2, 3]");
    want("at most two keeps the newest two", Refresher.past(times, now, 0, 2).toString(), "[2, 3]");
    want("either limit drops", Refresher.past(times, now, 3, 1).toString(), "[1, 2, 3]");
    want("no limit keeps all", Refresher.past(times, now, 0, 0).toString(), "[]");
    want("a book a week old to the second stays", Refresher.past(new long[] {now - 7 * day}, now, 7, 0).toString(), "[]");
    want("a book of no known time is not old", Refresher.past(new long[] {0}, now, 7, 0).toString(), "[]");
    want("for ever wins over a week", String.valueOf(Refresher.looser(0, 7)), "0");
    want("a week wins over a day", String.valueOf(Refresher.looser(1, 7)), "7");
    Feeds.Feed f = new Feeds.Feed("Go", "https://go.dev/blog/feed.atom", "", "");
    want("a new feed keeps every book", MainActivity.keeps(f), "keeps for ever");
    f.keepDays = 0;
    f.keepMost = 50;
    f.together = true;
    f.mute = "foot";
    want("a feed's settings in words", MainActivity.keeps(f), "keeps for ever, at most 50, one book per refresh, words picked");
    want("a hash is 16 hex digits", String.valueOf(Refresher.hash("https://x/a").matches("[0-9a-f]{16}")), "true");
    if (failed > 0) System.exit(1);
    System.out.println("rules: all hold");
  }
}
